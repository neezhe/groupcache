/*
Copyright 2013 Google Inc.

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

// Package lru implements an LRU cache.
package lru

//所谓LRU其实就是操作系统里那个内存页管理的经典算法——最近最少被使用（Least Recently Used Algorithm）。
// 其实除了操作系统底层，很多数据库或者缓存产品里都实现了LRU，例如Innodb存储引擎的buffer pool里的LRU List就是一个关键数据结构。

import "container/list"
//cache结构，数据存放在一个双向链表中，并提供一个map映射到key跟列表的元素，链表主要提供lru算法。map主要提供快速查找key
// Cache is an LRU cache. It is not safe for concurrent access.
type Cache struct { // LRU的高层封装（非并发安全！）
	// MaxEntries is the maximum number of cache entries before
	// an item is evicted. Zero means no limit.
	MaxEntries int   // 最多允许存多少个K-V entry

	// OnEvicted optionally specifies a callback function to be
	// executed when an entry is purged from the cache.
	OnEvicted func(key Key, value interface{})  // 数据项被淘汰时，回调函数，当一个entry被移除后回调
	//下面用了一个map来做查找，用ll来做lru刷新
	ll    *list.List //LRU双向链表。维护数据的访问次序.这个是标准库。
	cache map[interface{}]*list.Element //Element是标准库中代表双链表的元素// 记录Key -> entry的映射关系（Element中的value存的是entry,），O(1)时间得到entry。所有我们需要根据key拿到的值就存在这个里面。
}

// A Key may be any value that is comparable. See http://golang.org/ref/spec#Comparison_operators
type Key interface{} //Key是任意可比较（Comparable）类型

type entry struct { // 一个 entry 包含一个 key 和一个 value，都是任意类型
	key   Key
	value interface{}
}

// New creates a new Cache.
// If maxEntries is zero, the cache has no limit and it's assumed
// that eviction is done by the caller.
func New(maxEntries int) *Cache {
	return &Cache{
		MaxEntries: maxEntries,  //若maxEntries为0则表示缓存没有大小限制
		ll:         list.New(),  //list是这个双向链表的头，Element是链表中的节点.
		cache:      make(map[interface{}]*list.Element),
	}
}
// Add方法，插入一个K-V对
func (c *Cache) Add(key Key, value interface{}) {
	if c.cache == nil { //若事先没有根据maxEntries来New一个Cache,那么此处就初始化一个大小没有限制的Cache（即MaxEntries为0的情况）
		c.cache = make(map[interface{}]*list.Element)
		c.ll = list.New()  //标准库中的新建
	}
	if ee, ok := c.cache[key]; ok { // 如果该key已存在，更新entry里的value值，并将entry挪到链表头部
		c.ll.MoveToFront(ee) //把这个节点移到头部
		ee.Value.(*entry).value = value //修改这个节点的值
		return
	}
	ele := c.ll.PushFront(&entry{key, value}) // 如果该key不存在，新建一个entry，插到链表头部，插入的数据结构为entry，存到element,然后放到链表前面
	c.cache[key] = ele
	if c.MaxEntries != 0 && c.ll.Len() > c.MaxEntries { // 如果超出链表允许长度，移除链表尾部的数据
		c.RemoveOldest()
	}
}

// Get looks up a key's value from the cache.
func (c *Cache) Get(key Key) (value interface{}, ok bool) {// Get方法，通过Key来拿对应的value
	if c.cache == nil {
		return
	}
	if ele, hit := c.cache[key]; hit { //如果该key存在，获取对应entry的value，将该entry挪到链表头部，返回。
		c.ll.MoveToFront(ele)
		return ele.Value.(*entry).value, true
	}
	return
}

// Remove removes the provided key from the cache.
func (c *Cache) Remove(key Key) {
	if c.cache == nil {
		return
	}
	if ele, hit := c.cache[key]; hit {
		c.removeElement(ele)
	}
}

// RemoveOldest removes the oldest item from the cache.
func (c *Cache) RemoveOldest() {  //删除最老的这个元素
	if c.cache == nil {
		return
	}
	ele := c.ll.Back()
	if ele != nil {
		c.removeElement(ele)
	}
}

func (c *Cache) removeElement(e *list.Element) {
	c.ll.Remove(e)
	kv := e.Value.(*entry)
	delete(c.cache, kv.key)
	if c.OnEvicted != nil {
		c.OnEvicted(kv.key, kv.value)
	}
}

// Len returns the number of items in the cache.
func (c *Cache) Len() int {
	if c.cache == nil {
		return 0
	}
	return c.ll.Len()
}

// Clear purges all stored items from the cache.
func (c *Cache) Clear() {
	if c.OnEvicted != nil {
		for _, e := range c.cache {
			kv := e.Value.(*entry)
			c.OnEvicted(kv.key, kv.value)
		}
	}
	c.ll = nil
	c.cache = nil
}
