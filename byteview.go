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
	"bytes"
	"errors"
	"io"
	"strings"
)

// A ByteView holds an immutable view of bytes.
// Internally it wraps either a []byte or a string,
// but that detail is invisible to callers.
//
// A ByteView is meant to be used as a value type, not
// a pointer (like a time.Time).
type ByteView struct {
	// If b is non-nil, b is used, else s is used.
	b []byte //如果b非空则使用b,反之使用s
	s string
}

// Len returns the view's length.
func (v ByteView) Len() int { //返回一个view的长度
	if v.b != nil {
		return len(v.b)
	}
	return len(v.s)
}

// ByteSlice returns a copy of the data as a byte slice.
func (v ByteView) ByteSlice() []byte { //获取一份[]byte类型的view值的拷贝
	if v.b != nil {
		return cloneBytes(v.b)
	}
	return []byte(v.s)
}

// String returns the data as a string, making a copy if necessary.
func (v ByteView) String() string { //上一个是返回[]byte类型，这里是string类型
	if v.b != nil {
		return string(v.b)
	}
	return v.s
}

// At returns the byte at index i.
func (v ByteView) At(i int) byte { //返回第i个byte
	if v.b != nil {
		return v.b[i]
	}
	return v.s[i]   //字符串索引获取到的值是byte类型，因为一个字符也是uint8
}

// Slice slices the view between the provided from and to indices.
func (v ByteView) Slice(from, to int) ByteView { //返回从索引from到to的view的切分结果
	if v.b != nil {
		return ByteView{b: v.b[from:to]}
	}
	return ByteView{s: v.s[from:to]}
}

// SliceFrom slices the view from the provided index until the end.
func (v ByteView) SliceFrom(from int) ByteView { //相当于上面的to为len(b)
	if v.b != nil {
		return ByteView{b: v.b[from:]}
	}
	return ByteView{s: v.s[from:]}
}

// Copy copies b into dest and returns the number of bytes copied.
func (v ByteView) Copy(dest []byte) int { //拷贝一份view到dest
	if v.b != nil {
		return copy(dest, v.b)
	}
	return copy(dest, v.s)
}

// Equal returns whether the bytes in b are the same as the bytes in
// b2.
func (v ByteView) Equal(b2 ByteView) bool { //相等判断，具体比较的实现在下面
	if b2.b == nil {
		return v.EqualString(b2.s)
	}
	return v.EqualBytes(b2.b)
}

// EqualString returns whether the bytes in b are the same as the bytes
// in s.
func (v ByteView) EqualString(s string) bool {
	if v.b == nil { //如果b为nil，则比较s是否相等
		return v.s == s
	}
	l := v.Len()  //l为view的长度，实现在上面
	if len(s) != l {   //长度不同直接返回false
		return false
	}
	for i, bi := range v.b { //【判断[]byte中的每一个byte是否和string的每一个字符相等】
		if bi != s[i] {
			return false
		}
	}
	return true
}

// EqualBytes returns whether the bytes in b are the same as the bytes
// in b2.
func (v ByteView) EqualBytes(b2 []byte) bool {
	if v.b != nil { //【b不为空，直接通过Equal方法比较】
		return bytes.Equal(v.b, b2)
	}
	l := v.Len()
	if len(b2) != l { //【长度不等直接返回false】
		return false
	}
	for i, bi := range b2 {  //【与上一个方法类似，比较字符串和[]byte每一个byte是否相等】
		if bi != v.s[i] {
			return false
		}
	}
	return true
}

// Reader returns an io.ReadSeeker for the bytes in v.
//【返回值其实是bytes包或者strings包中的*Reader类型，这是个struct实现了io.ReadSeeker等接口】
func (v ByteView) Reader() io.ReadSeeker {
	if v.b != nil {
		return bytes.NewReader(v.b)
	}
	return strings.NewReader(v.s)
}

// ReadAt implements io.ReaderAt on the bytes in v.
func (v ByteView) ReadAt(p []byte, off int64) (n int, err error) {
	if off < 0 {
		return 0, errors.New("view: invalid offset")
	}
	if off >= int64(v.Len()) {
		return 0, io.EOF
	}
	n = v.SliceFrom(int(off)).Copy(p)  //【从off开始拷贝一份数据到p】
	if n < len(p) {
		err = io.EOF
	}
	return
}

// WriteTo implements io.WriterTo on the bytes in v.
//【将v写入w】
func (v ByteView) WriteTo(w io.Writer) (n int64, err error) {
	var m int
	if v.b != nil {
		m, err = w.Write(v.b)
	} else {
		m, err = io.WriteString(w, v.s)
	}
	if err == nil && m < v.Len() {
		err = io.ErrShortWrite
	}
	n = int64(m)
	return
}
