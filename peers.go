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

// peers.go defines how processes find and communicate with their peers.

package groupcache

import (
	pb "groupcache/groupcachepb"
)

// Context is an opaque value passed through calls to the
// ProtoGetter. It may be nil if your ProtoGetter implementation does
// not require a context.
type Context interface{}

// ProtoGetter is the interface that must be implemented by a peer.
type ProtoGetter interface {
	Get(context Context, in *pb.GetRequest, out *pb.GetResponse) error
}

// PeerPicker is the interface that must be implemented to locate
// the peer that owns a specific key.
type PeerPicker interface {
	// PickPeer returns the peer that owns the specific key
	// and true to indicate that a remote peer was nominated.
	// It returns nil, false if the key owner is the current peer.
	PickPeer(key string) (peer ProtoGetter, ok bool)
}

// NoPeers is an implementation of PeerPicker that never finds a peer.
type NoPeers struct{}

func (NoPeers) PickPeer(key string) (peer ProtoGetter, ok bool) { return }

//这个portPicker就是
//func (_ string) PeerPicker {
//	return func() PeerPicker {
//		return p
//	}
//}
//所以需要看一下PeerPicker和HTTPPool的关系，HTTPPool实现了PickPeer方法，所以HTTPPool是PeerPicker接口类型的。

var (
	portPicker func(groupName string) PeerPicker //函数，根据group名拿到对等节点拾取器，其实拿到的就是NewHTTPPool创建的那个HTTPPool结构体
)

// RegisterPeerPicker registers the peer initialization function.
// It is called once, when the first group is created.
// Either RegisterPeerPicker or RegisterPerGroupPeerPicker should be
// called exactly once, but not both.
func RegisterPeerPicker(fn func() PeerPicker) {
	if portPicker != nil { //这个变量是这个包中全局的，只被初始化一次
		panic("RegisterPeerPicker called more than once")
	}
	//注意此处的fn带括号了，表面这个函数是会运算的，而不是fn的指针
	portPicker = func(_ string) PeerPicker { return fn() } //这样的话,输入参数是什么就不会起作用。也就是说无论输入什么都会拿到这个PeerPicker
}

// RegisterPerGroupPeerPicker registers the peer initialization function,
// which takes the groupName, to be used in choosing a PeerPicker.
// It is called once, when the first group is created.
// Either RegisterPeerPicker or RegisterPerGroupPeerPicker should be
// called exactly once, but not both.
func RegisterPerGroupPeerPicker(fn func(groupName string) PeerPicker) {
	if portPicker != nil {
		panic("RegisterPeerPicker called more than once")
	}
	portPicker = fn
}

func getPeers(groupName string) PeerPicker {
	if portPicker == nil {
		return NoPeers{}
	}
	pk := portPicker(groupName) //根据group名拿到对等节点拾取器，但是在这个例子中，无论groupName是什么，拿到的都是前面base peer生成的HTTPPool
	if pk == nil {
		pk = NoPeers{}
	}
	return pk
}
