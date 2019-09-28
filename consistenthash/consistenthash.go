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

// Package consistenthash provides an implementation of a ring hash.
package consistenthash

import (
	"hash/crc32"
	"sort"
	"strconv"
)

//此中代码为在groupcache里用到一致性哈希的地方，就是多节点部署时，要把多个节点地址用一致性哈希管理起来，
// 从而让缓存数据能够均匀分散，降低单台服务器的压力。
//但是这里实现的一致性哈希还比较粗糙，没有实现动态删除节点，还不支持节点宕机后自动数据迁移，
// 这两个功能是一致性哈希的另一大精髓。（感兴趣的可参考我之前的文章）

type Hash func(data []byte) uint32 // Hash就是一个返回unit32的哈希方法
//Map结构中replicas的含义是增加虚拟节点，使数据分布更加均匀
// Map就是一致性哈希的高级封装
type Map struct {
	hash     Hash // 哈希函数
	replicas int // replica参数，表明了一份数据要冗余存储多少份,就是说多少个虚拟节点
	keys     []int // 存储key的hash值（包括虚拟节点的），按hash值升序排列（模拟一致性哈希环空间）
	hashMap  map[int]string // 记录key的hash值（由于有多个虚拟节点，所以这个有多个） ->key的真实值（比如节点ip地址），所以可能“010.1.10.3”和“110.1.10.3”和“210.1.10.3”的哈希值对应的原始key为“10.1.10.3”，
}
// 一致性哈希的工厂方法
func New(replicas int, fn Hash) *Map {
	m := &Map{
		replicas: replicas,
		hash:     fn, //传入的哈希函数
		hashMap:  make(map[int]string), //map在用之前必须先初始化
	} //m.keys和m.hashMap[hash]在下面Add中被填充
	if m.hash == nil {
		m.hash = crc32.ChecksumIEEE //nsq中也用到了这玩意，表示不指定自定义Hash方法的话，默认用ChecksumIEEE
	}
	return m
}

// Returns true if there are no items available.
func (m *Map) IsEmpty() bool {
	return len(m.keys) == 0
}

// Adds some keys to the hash.
// 添加新的Key，参数一般就是多个节点的ip地址（或者节点id）
func (m *Map) Add(keys ...string) {
	for _, key := range keys {
		for i := 0; i < m.replicas; i++ { // 每一个key都会冗余多份（每份冗余就是一致性哈希里的虚拟节点 v-node）
			hash := int(m.hash([]byte(strconv.Itoa(i) + key))) //虚拟节点的key的哈希值
			m.keys = append(m.keys, hash) //若有3个节点，最终m.keys就有了3乘以m.replicas个元素
			m.hashMap[hash] = key
		}
	}
	sort.Ints(m.keys)//一致性哈希要求哈希环是升序的，执行一次排序操作
}

// Gets the closest item in the hash to the provided key.
// 根据hash(key)获取value，找到该key应该存于哪个节点，返回该节点的地址
func (m *Map) Get(key string) string { //这个key是啥玩意?可能是要根据图片名来拿到存储在哪台服务器上的地址。
	if m.IsEmpty() {
		return ""
	}
	// 1. 算出key的hash值
	// 2. 二分查找大于等于该key的第一个hash值的下标（哈希环是升序有序的，所以可以二分查找）
	hash := int(m.hash([]byte(key)))

	// Binary search for appropriate replica.
	//Search 常用于在一个已排序的，可索引的数据结构中寻找索引为 i 的值 x，例如数组或切片。
	idx := sort.Search(len(m.keys), func(i int) bool { return m.keys[i] >= hash })//内部实现二分查找，查找条件由第二个参数指定，返回查找到的索引

	// Means we have cycled back to the first replica.
	if idx == len(m.keys) {//在切片中无法找到使第二个参数为true的i时，sort.Search的返回值为Search的第一个参数
		idx = 0 //下标越界，循环找到到0号下标
	}

	return m.hashMap[m.keys[idx]] // 通过hash值，得到节点地址
}
