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

// Package groupcache provides a data loading mechanism with caching
// and de-duplication that works across a set of peer processes.
//
// Each data Get first consults its local cache, otherwise delegates
// to the requested key's canonical owner, which then checks its cache
// or finally gets the data.  In the common case, many concurrent
// cache misses across a set of peers for the same key result in just
// one cache fill.
package groupcache

import (
	"errors"
	"math/rand"
	"strconv"
	"sync"
	"sync/atomic"

	pb "groupcache/groupcachepb"
	"groupcache/lru"
	"groupcache/singleflight"
)

// A Getter loads data for a key.
type Getter interface {
	// Get returns the value identified by key, populating dest.
	//
	// The returned data must be unversioned. That is, key must
	// uniquely describe the loaded data, without an implicit
	// current time, and without relying on cache expiration
	// mechanisms.
	Get(ctx Context, key string, dest Sink) error
}

// A GetterFunc implements Getter with a function.
type GetterFunc func(ctx Context, key string, dest Sink) error

func (f GetterFunc) Get(ctx Context, key string, dest Sink) error {
	return f(ctx, key, dest)
}

var (
	mu     sync.RWMutex
	groups = make(map[string]*Group)

	initPeerServerOnce sync.Once //内部使用原子计数记录被执行的次数，保证只执行一次。无论是否更换once.Do(xx)这里的方法,这个sync.Once块只会执行一次。
	initPeerServer     func()
)

// GetGroup returns the named group previously created with NewGroup, or
// nil if there's no such group.
func GetGroup(name string) *Group {
	mu.RLock()
	g := groups[name]
	mu.RUnlock()
	return g
}

// NewGroup creates a coordinated group-aware Getter from a Getter.
//
// The returned Getter tries (but does not guarantee) to run only one
// Get call at once for a given key across an entire set of peer
// processes. Concurrent callers both in the local process and in
// other processes receive copies of the answer once the original Get
// completes.
//
// The group name must be unique for each getter.
func NewGroup(name string, cacheBytes int64, getter Getter) *Group {
	return newGroup(name, cacheBytes, getter, nil)
}

// If peers is nil, the peerPicker is called via a sync.Once to initialize it.
func newGroup(name string, cacheBytes int64, getter Getter, peers PeerPicker) *Group {
	if getter == nil { //需要先判断一下这个分组存在与否,重复创建分组,会panic.
		panic("nil Getter")
	}
	mu.Lock()
	defer mu.Unlock()
	initPeerServerOnce.Do(callInitPeerServer) //initPeerServerOnce只会被执行一次，无论修饰的是什么函数。callInitPeerServer是group创建的时候要调用的钩子函数
	if _, dup := groups[name]; dup {          //组名必须唯一，是个map
		panic("duplicate registration of group " + name)
	}
	g := &Group{
		name:       name,
		getter:     getter,
		peers:      peers, //nil
		cacheBytes: cacheBytes,
		loadGroup:  &singleflight.Group{},
	}
	if fn := newGroupHook; fn != nil {
		fn(g)
	}
	groups[name] = g
	return g
}

// newGroupHook, if non-nil, is called right after a new group is created.
var newGroupHook func(*Group)

// RegisterNewGroupHook registers a hook that is run each time
// a group is created.
func RegisterNewGroupHook(fn func(*Group)) {
	if newGroupHook != nil {
		panic("RegisterNewGroupHook called more than once")
	}
	newGroupHook = fn
}

// RegisterServerStart registers a hook that is run when the first
// group is created.
func RegisterServerStart(fn func()) {
	if initPeerServer != nil {
		panic("RegisterServerStart called more than once")
	}
	initPeerServer = fn
}

func callInitPeerServer() {
	if initPeerServer != nil {
		initPeerServer()
	}
}

// A Group is a cache namespace and associated data loaded spread over
// a group of 1 or more machines.
type Group struct {
	name   string //group的名字，必须唯一
	getter Getter //getter方法，用于从缓存中没找到则从数据库或其他地方获取数据.
	//分布式支持
	peersOnce  sync.Once
	peers      PeerPicker // 用于获取peer，节点调度器
	cacheBytes int64      // mainCache和hotCache的总大小限制

	// mainCache is a cache of the keys for which this process
	// (amongst its peers) is authoritative. That is, this cache
	// contains keys which consistent hash on to this process's
	// peer number.
	mainCache cache // 此节点的缓存

	// hotCache contains keys/values for which this peer is not
	// authoritative (otherwise they would be in mainCache), but
	// are popular enough to warrant mirroring in this process to
	// avoid going over the network to fetch from a peer.  Having
	// a hotCache avoids network hotspotting, where a peer's
	// network card could become the bottleneck on a popular key.
	// This cache is used sparingly to maximize the total number
	// of key/value pairs that can be stored globally.
	hotCache cache // 其他节点的缓存

	// loadGroup ensures that each key is only fetched once
	// (either locally or remotely), regardless of the number of
	// concurrent callers.
	//比如一个缓存数据失效了，这个时候同时会有很多人调用接口，缓存都没有命中，就会对数据库发起很多次调用，
	//其实这个时候只要调用一次就行了，其他的都是相同的数据。
	loadGroup flightGroup // 在缓存命中失败的时候减少调用,避免同一时刻对同一Key值得重复请求，请求并发控制器

	_ int32 // force Stats to be 8-byte aligned on 32-bit platforms

	// Stats are statistics on the group.
	Stats Stats
}

// flightGroup is defined as an interface which flightgroup.Group
// satisfies.  We define this so that we may test with an alternate
// implementation.
type flightGroup interface {
	// Done is called when Do is done.
	Do(key string, fn func() (interface{}, error)) (interface{}, error)
}

// Stats are per-group statistics.
type Stats struct {
	Gets           AtomicInt // any Get request, including from peers
	CacheHits      AtomicInt // either cache was good
	PeerLoads      AtomicInt // either remote load or remote cache hit (not an error)
	PeerErrors     AtomicInt
	Loads          AtomicInt // (gets - cacheHits)
	LoadsDeduped   AtomicInt // after singleflight
	LocalLoads     AtomicInt // total good local loads
	LocalLoadErrs  AtomicInt // total bad local loads
	ServerRequests AtomicInt // gets that came over the network from peers
}

// Name returns the name of the group.
func (g *Group) Name() string {
	return g.name
}

func (g *Group) initPeers() {
	if g.peers == nil {
		g.peers = getPeers(g.name) //拿到HTTPPool
	}
}

//sink就是洗涤池，这表示这个东西可以存放各种类型的cache值。总共有5个池子：allocateByteSink,byteViewSink...
func (g *Group) Get(ctx Context, key string, dest Sink) error {
	g.peersOnce.Do(g.initPeers) //初始化Group结构体的对等节点拾取器
	g.Stats.Gets.Add(1)
	if dest == nil {
		return errors.New("groupcache: nil dest Sink")
	}
	value, cacheHit := g.lookupCache(key) //在缓存中查看是否有，包括mainCache和hotCache.第一次肯定是找不到的,第一次必须从磁盘拿到。

	if cacheHit { //是否命中
		g.Stats.CacheHits.Add(1)
		return setSinkView(dest, value)
	}

	// Optimization to avoid double unmarshalling or copying: keep
	// track of whether the dest was already populated. One caller
	// (if local) will set this; the losers will not. The common
	// case will likely be one caller.
	destPopulated := false
	value, destPopulated, err := g.load(ctx, key, dest) //如果没有在缓存中找到数据，就从getter方法中load进来,就是NewGroup的第三个方法。
	if err != nil {
		return err
	}
	if destPopulated { //若dest已经被填充
		return nil
	}
	return setSinkView(dest, value)
}

// load loads key either by invoking the getter locally or by sending it to another machine.
// 获取数据，从本地或者其它机器
func (g *Group) load(ctx Context, key string, dest Sink) (value ByteView, destPopulated bool, err error) {
	g.Stats.Loads.Add(1)
	//loadGroup减少对底层的调用，上面已经说了
	//哈哈，调用的是singleflight.Group的Do方法，不是orderFlightGroup的。注意groupcache中的Group和singleflight中的Group不一样。
	//这个loadGroup在前面创建Group的时候只是初始化为0值
	viewi, err := g.loadGroup.Do(key, func() (interface{}, error) {
		// Check the cache again because singleflight can only dedup calls
		// that overlap concurrently.  It's possible for 2 concurrent
		// requests to miss the cache, resulting in 2 load() calls.  An
		// unfortunate goroutine scheduling would result in this callback
		// being run twice, serially.  If we don't check the cache again,
		// cache.nbytes would be incremented below even though there will
		// be only one entry for this key.
		//
		// Consider the following serialized event ordering for two
		// goroutines in which this callback gets called twice for the
		// same key:
		// 1: Get("key")
		// 2: Get("key")
		// 1: lookupCache("key")
		// 2: lookupCache("key")
		// 1: load("key")
		// 2: load("key")
		// 1: loadGroup.Do("key", fn)
		// 1: fn()
		// 2: loadGroup.Do("key", fn)
		// 2: fn()
		if value, cacheHit := g.lookupCache(key); cacheHit {
			g.Stats.CacheHits.Add(1)
			return value, nil
		}
		g.Stats.LoadsDeduped.Add(1)
		var value ByteView
		var err error
		if peer, ok := g.peers.PickPeer(key); ok { //如果能从远程获取，就从分布式的其他机子获取，因为其他机器也是缓存数据比数据库快.其实就是HTTPPool的PickPeer函数。
			value, err = g.getFromPeer(ctx, peer, key) //第二个参数是httpGetter类型
			if err == nil {
				g.Stats.PeerLoads.Add(1)
				return value, nil
			}
			g.Stats.PeerErrors.Add(1)
			// TODO(bradfitz): log the peer's error? keep
			// log of the past few for /groupcachez?  It's
			// probably boring (normal task movement), so not
			// worth logging I imagine.
		}
		value, err = g.getLocally(ctx, key, dest) //调用getter方法，获取数据(从数据库，或者其他地方)
		if err != nil {
			g.Stats.LocalLoadErrs.Add(1)
			return nil, err
		}
		g.Stats.LocalLoads.Add(1)
		destPopulated = true                      // only one caller of load gets this return value
		g.populateCache(key, value, &g.mainCache) //把数据存放在cache中
		return value, nil
	})
	if err == nil {
		value = viewi.(ByteView)
	}
	return
}

func (g *Group) getLocally(ctx Context, key string, dest Sink) (ByteView, error) {
	err := g.getter.Get(ctx, key, dest)
	if err != nil {
		return ByteView{}, err
	}
	return dest.view()
}

// 从其它机器获取数据.每一个分布式的服务都需要实现一个Get方法，接口描述文件在proto文件中
func (g *Group) getFromPeer(ctx Context, peer ProtoGetter, key string) (ByteView, error) {
	req := &pb.GetRequest{
		Group: &g.name,
		Key:   &key,
	}
	res := &pb.GetResponse{}
	err := peer.Get(ctx, req, res) //从远端得到数据
	if err != nil {
		return ByteView{}, err
	}
	value := ByteView{b: res.Value}
	// TODO(bradfitz): use res.MinuteQps or something smart to
	// conditionally populate hotCache.  For now just do it some
	// percentage of the time.
	if rand.Intn(10) == 0 { //哈哈，这里随机放在hotCache中,有意思
		g.populateCache(key, value, &g.hotCache)
	}
	return value, nil
}

//这个方法比较简单，从是从maincache和hotcache中读取数据
func (g *Group) lookupCache(key string) (value ByteView, ok bool) {
	if g.cacheBytes <= 0 {
		return
	}
	//语法：没有显式初始化的结构体变量都会自动初始化为相应类型的零值，下面mainCache，虽然在前面没有被显式初始化，但是是可以调用get方法的。
	value, ok = g.mainCache.get(key)
	if ok {
		return
	}
	value, ok = g.hotCache.get(key)
	return
}

func (g *Group) populateCache(key string, value ByteView, cache *cache) {
	if g.cacheBytes <= 0 {
		return
	}
	cache.add(key, value)

	// Evict items from cache(s) if necessary.
	for {
		mainBytes := g.mainCache.bytes()
		hotBytes := g.hotCache.bytes()
		if mainBytes+hotBytes <= g.cacheBytes {
			return
		}

		// TODO(bradfitz): this is good-enough-for-now logic.
		// It should be something based on measurements and/or
		// respecting the costs of different resources.
		victim := &g.mainCache
		if hotBytes > mainBytes/8 {
			victim = &g.hotCache
		}
		victim.removeOldest()
	}
}

// CacheType represents a type of cache.
type CacheType int

const (
	// The MainCache is the cache for items that this peer is the
	// owner for.
	MainCache CacheType = iota + 1

	// The HotCache is the cache for items that seem popular
	// enough to replicate to this node, even though it's not the
	// owner.
	HotCache
)

// CacheStats returns stats about the provided cache within the group.
func (g *Group) CacheStats(which CacheType) CacheStats {
	switch which {
	case MainCache:
		return g.mainCache.stats()
	case HotCache:
		return g.hotCache.stats()
	default:
		return CacheStats{}
	}
}

// cache is a wrapper around an *lru.Cache that adds synchronization,
// makes values always be ByteView, and counts the size of all keys and
// values.
//groupcache中的cache主要是加了并发安全，并添加一些统计数据, 一些操作都是直接调用lru.Cache,显然cache由lru.Cache组合而来.
//注意这里面的cache和lru中的Cache不一样。
type cache struct {
	mu         sync.RWMutex
	nbytes     int64 //所有Key和Value的字节数
	lru        *lru.Cache
	nhit, nget int64
	nevict     int64 // number of evictions
}

func (c *cache) stats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return CacheStats{
		Bytes:     c.nbytes,
		Items:     c.itemsLocked(),
		Gets:      c.nget,
		Hits:      c.nhit,
		Evictions: c.nevict,
	}
}

// 往cache中添加键值对
func (c *cache) add(key string, value ByteView) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lru == nil {
		c.lru = &lru.Cache{ // 设置lru中的淘汰函数
			OnEvicted: func(key lru.Key, value interface{}) {
				val := value.(ByteView)
				c.nbytes -= int64(len(key.(string))) + int64(val.Len())
				c.nevict++
			},
		}
	}
	c.lru.Add(key, value)
	c.nbytes += int64(len(key)) + int64(value.Len())
}

func (c *cache) get(key string) (value ByteView, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nget++
	if c.lru == nil {
		return
	}
	vi, ok := c.lru.Get(key)
	if !ok {
		return
	}
	c.nhit++
	return vi.(ByteView), true
}

func (c *cache) removeOldest() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lru != nil {
		c.lru.RemoveOldest()
	}
}

func (c *cache) bytes() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.nbytes
}

func (c *cache) items() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.itemsLocked()
}

func (c *cache) itemsLocked() int64 {
	if c.lru == nil {
		return 0
	}
	return int64(c.lru.Len())
}

// An AtomicInt is an int64 to be accessed atomically.
type AtomicInt int64

// Add atomically adds n to i.
func (i *AtomicInt) Add(n int64) {
	atomic.AddInt64((*int64)(i), n)
}

// Get atomically gets the value of i.
func (i *AtomicInt) Get() int64 {
	return atomic.LoadInt64((*int64)(i))
}

func (i *AtomicInt) String() string {
	return strconv.FormatInt(i.Get(), 10)
}

// CacheStats are returned by stats accessors on Group.
type CacheStats struct {
	Bytes     int64
	Items     int64
	Gets      int64
	Hits      int64
	Evictions int64
}
