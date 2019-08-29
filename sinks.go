/*
Copyright 2012 Google Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package groupcache

import (
	"errors"

	"github.com/golang/protobuf/proto"
)

// A Sink receives data from a Get call.
//
// Implementation of Getter must call exactly one of the Set methods
// on success.
//实现Getter的时候必须调用这其中的某一个方法
type Sink interface { //SetXXX用来存储数据,view方法获取到一些东西，返回值是ByteView类型的。
	// SetString sets the value to s.
	SetString(s string) error

	// SetBytes sets the value to the contents of v.
	// The caller retains ownership of v.
	SetBytes(v []byte) error

	// SetProto sets the value to the encoded version of m.
	// The caller retains ownership of m.
	SetProto(m proto.Message) error

	// view returns a frozen view of the bytes for caching.
	view() (ByteView, error)
}

func cloneBytes(b []byte) []byte { //克隆一个byte切片
	c := make([]byte, len(b))
	copy(c, b)
	return c
}

func setSinkView(s Sink, v ByteView) error {
	// A viewSetter is a Sink that can also receive its value from
	// a ByteView. This is a fast path to minimize copies when the
	// item was already cached locally in memory (where it's
	// cached as a ByteView)
	type viewSetter interface {
		setView(v ByteView) error
	}
	//实现了setView方法的只有byteViewSink结构体和allocBytesSink结构体
	if vs, ok := s.(viewSetter); ok { //此处能够进行类型转换的话表示s是byteViewSink或allocBytesSink类型
		return vs.setView(v) //一般是分开处理ByteView中的b或者s，这里明显是不需要区分了，直接设置整个ByteView
	}
	//如果不是，则通过Sink的SetXxx()方法设置ByteView
	if v.b != nil {
		return s.SetBytes(v.b)
	}
	return s.SetString(v.s)
}

// StringSink returns a Sink that populates the provided string pointer.
func StringSink(sp *string) Sink { //根据传入的字符串指针初始化一个stringSink实例
	return &stringSink{sp: sp}
}

type stringSink struct {
	sp *string //字符串指针的缩写
	v  ByteView
	// TODO(bradfitz): track whether any Sets were called.
}

func (s *stringSink) view() (ByteView, error) { //获取stringSink的ByteView
	// TODO(bradfitz): return an error if no Set was called
	return s.v, nil
}

func (s *stringSink) SetString(v string) error {
	s.v.b = nil
	s.v.s = v
	*s.sp = v
	return nil
}

func (s *stringSink) SetBytes(v []byte) error {
	return s.SetString(string(v))
}

func (s *stringSink) SetProto(m proto.Message) error {
	b, err := proto.Marshal(m) //编码
	if err != nil {
		return err
	}
	s.v.b = b
	*s.sp = string(b)
	return nil
}

// ByteViewSink returns a Sink that populates a ByteView.
func ByteViewSink(dst *ByteView) Sink { //【根据参数dst构造byteViewSink结构体实例】
	if dst == nil {
		panic("nil dst")
	}
	return &byteViewSink{dst: dst}
}

type byteViewSink struct { //【属性dst为一个ByteView指针】
	dst *ByteView

	// if this code ever ends up tracking that at least one set*
	// method was called, don't make it an error to call set
	// methods multiple times. Lorry's payload.go does that, and
	// it makes sense. The comment at the top of this file about
	// "exactly one of the Set methods" is overly strict. We
	// really care about at least once (in a handler), but if
	// multiple handlers fail (or multiple functions in a program
	// using a Sink), it's okay to re-use the same one.
}

func (s *byteViewSink) setView(v ByteView) error {
	*s.dst = v
	return nil
}

func (s *byteViewSink) view() (ByteView, error) {
	return *s.dst, nil
}

func (s *byteViewSink) SetProto(m proto.Message) error { //【设置byteViewSink中ByteView的b】
	b, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	*s.dst = ByteView{b: b}
	return nil
}

func (s *byteViewSink) SetBytes(b []byte) error { //【复制b，初始化byteViewSink的dst】
	*s.dst = ByteView{b: cloneBytes(b)}
	return nil
}

func (s *byteViewSink) SetString(v string) error { //【通过使用string类型的v初始化一个ByteView后初始化byteViewSink的dst】
	*s.dst = ByteView{s: v}
	return nil
}

// ProtoSink returns a sink that unmarshals binary proto values into m.
func ProtoSink(m proto.Message) Sink { //【使用proto.Message类型的m初始化protoSink的dst】
	return &protoSink{
		dst: m,
	}
}

type protoSink struct {
	dst proto.Message // authoritative value
	typ string

	v ByteView // encoded
}

func (s *protoSink) view() (ByteView, error) { //【返回protoSink的ByteView类型的v】
	return s.v, nil
}

func (s *protoSink) SetBytes(b []byte) error { //【将s.dst反序列化后丢给b，并且复制一份丢给protoSink中ByteView的b】
	err := proto.Unmarshal(b, s.dst)
	if err != nil {
		return err
	}
	s.v.b = cloneBytes(b)
	s.v.s = ""
	return nil
}

func (s *protoSink) SetString(v string) error { //【将b解码后写入s.dst】
	b := []byte(v)
	err := proto.Unmarshal(b, s.dst)
	if err != nil {
		return err
	}
	s.v.b = b
	s.v.s = ""
	return nil
}

func (s *protoSink) SetProto(m proto.Message) error { //【将m写入protoSink的dst】
	b, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	// TODO(bradfitz): optimize for same-task case more and write
	// right through? would need to document ownership rules at
	// the same time. but then we could just assign *dst = *m
	// here. This works for now:
	err = proto.Unmarshal(b, s.dst)
	if err != nil {
		return err
	}
	s.v.b = b
	s.v.s = ""
	return nil
}

// AllocatingByteSliceSink returns a Sink that allocates
// a byte slice to hold the received value and assigns
// it to *dst. The memory is not retained by groupcache.
func AllocatingByteSliceSink(dst *[]byte) Sink { //【分配一个字节切片来保存接收到的数据】
	return &allocBytesSink{dst: dst}
}

type allocBytesSink struct { //【有一个字节切片指针类型的属性dst】
	dst *[]byte
	v   ByteView
}

func (s *allocBytesSink) view() (ByteView, error) {
	return s.v, nil
}

func (s *allocBytesSink) setView(v ByteView) error { //【设置allocBytesSink的v，同时复制v中的b或者s丢给dst】
	if v.b != nil {
		*s.dst = cloneBytes(v.b)
	} else {
		*s.dst = []byte(v.s)
	}
	s.v = v
	return nil
}

func (s *allocBytesSink) SetProto(m proto.Message) error { //【这个得从下面的setBytesOwned开始往上看】
	b, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	return s.setBytesOwned(b)
}

func (s *allocBytesSink) SetBytes(b []byte) error { //【复制一份b，然后调用setBytesOwned】
	return s.setBytesOwned(cloneBytes(b))
}

func (s *allocBytesSink) setBytesOwned(b []byte) error { //【使用b设置allocBytesSink的dst和ByteView】
	if s.dst == nil {
		return errors.New("nil AllocatingByteSliceSink *[]byte dst")
	}
	*s.dst = cloneBytes(b) // another copy, protecting the read-only s.v.b view
	s.v.b = b
	s.v.s = ""
	return nil
}

func (s *allocBytesSink) SetString(v string) error {
	if s.dst == nil {
		return errors.New("nil AllocatingByteSliceSink *[]byte dst")
	}
	*s.dst = []byte(v)
	s.v.b = nil
	s.v.s = v
	return nil
}

// TruncatingByteSliceSink returns a Sink that writes up to len(*dst)
// bytes to *dst. If more bytes are available, they're silently
// truncated. If fewer bytes are available than len(*dst), *dst
// is shrunk to fit the number of bytes available.
func TruncatingByteSliceSink(dst *[]byte) Sink { //【截面字节切片Sink，写入len(*dst)个字节到dst，如果长度超了会被截断，少了则dst会收缩长度来适配】
	return &truncBytesSink{dst: dst}
}

type truncBytesSink struct {
	dst *[]byte
	v   ByteView
}

func (s *truncBytesSink) view() (ByteView, error) {
	return s.v, nil
}

func (s *truncBytesSink) SetProto(m proto.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	return s.setBytesOwned(b)
}

func (s *truncBytesSink) SetBytes(b []byte) error {
	return s.setBytesOwned(cloneBytes(b))
}

func (s *truncBytesSink) setBytesOwned(b []byte) error {
	if s.dst == nil {
		return errors.New("nil TruncatingByteSliceSink *[]byte dst")
	}
	n := copy(*s.dst, b)
	if n < len(*s.dst) {
		*s.dst = (*s.dst)[:n]
	}
	s.v.b = b
	s.v.s = ""
	return nil
}

func (s *truncBytesSink) SetString(v string) error {
	if s.dst == nil {
		return errors.New("nil TruncatingByteSliceSink *[]byte dst")
	}
	n := copy(*s.dst, v)
	if n < len(*s.dst) {
		*s.dst = (*s.dst)[:n]
	}
	s.v.b = nil
	s.v.s = v
	return nil
}
