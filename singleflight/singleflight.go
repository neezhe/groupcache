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

// Package singleflight provides a duplicate function call suppression
// mechanism.
package singleflight

import "sync"

// call is an in-flight or completed Do call
type call struct { // call等价于一条被真正执行的对某个key的查询操作
	wg  sync.WaitGroup // 用于阻塞对某个key的多条查询命令，同一时刻只能有1条真正执行的查询命令
	val interface{} // 查询结果，也就是缓存中某个key对应的value值
	err error
}

// Group represents a class of work and forms a namespace in which
// units of work can be executed with duplicate suppression.
type Group struct { // Group相当于一个管理每个key的call请求的对象
	mu sync.Mutex       // 并发情况下，保证m这个普通map不会有并发安全问题
	m  map[string]*call // key为数据的key(非hash的)，value为一条call命令，记录下某个key当前时刻有没有客户端在查询
}

// Do executes and returns the results of the given function, making
// sure that only one execution is in-flight for a given key at a
// time. If a duplicate comes in, the duplicate caller waits for the
// original to complete and receives the same results.
// Do里面是查询命令执行的逻辑。
// 当客户端想查询某个key对应的值时会调用Do方法来执行查询。
// 参数传入一个待查询的key，还有一个对应的查询方法，返回key对应的value值
func (g *Group) Do(key string, fn func() (interface{}, error)) (interface{}, error) {
	g.mu.Lock() // 为了保证普通map的并发安全，要先上锁
	if g.m == nil { // 检查map有无初始化
		g.m = make(map[string]*call)
	}
	// 检查当前时刻，该key是否已经有别的客户端在查询
	// 如果有别的客户端也正在查询，map里肯定存有该key，以及一条对应的call命令
	if c, ok := g.m[key]; ok {
		g.mu.Unlock() // 解锁，自己准备阻塞，此时已不存在并发安全问题，允许别人进行查询
		c.wg.Wait() // 阻塞，等待别的客户端完成查询就好，不用自己再去耗费资源查询
		return c.val, c.err  // 阻塞结束，说明别人已经查询完成，拿来主义直接返回
	}
	// 如果能执行到此步，说明当前时刻没有别人在查询该key，当前客户端是
	// 当前时刻第一个想要查询该key的人，就插入一条key -> call记录
	// 注意，此时的map仍然是上锁状态，因为还要对map进行插入，有并发安全问题
	c := new(call)
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()
	// 执行作为参数传入的查询方法
	// **同一时刻对于同一个key只可能有一个客户端执行到此处**
	c.val, c.err = fn() //获取数据
	c.wg.Done()

	g.mu.Lock()
	delete(g.m, key) // 执行完查询方法，把map中的key -> call删掉
	g.mu.Unlock()

	return c.val, c.err
}
