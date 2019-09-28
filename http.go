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

package groupcache

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/golang/groupcache/consistenthash"
	pb "github.com/golang/groupcache/groupcachepb"
	"github.com/golang/protobuf/proto"
)

const defaultBasePath = "/_groupcache/"

const defaultReplicas = 50

// HTTPPool implements PeerPicker for a pool of HTTP peers.
type HTTPPool struct {
	// Context optionally specifies a context for the server to use when it
	// receives a request.
	// If nil, the server uses a nil Context.
	Context func(*http.Request) Context // 可选，为每次的请求封装的Context参数

	// Transport optionally specifies an http.RoundTripper for the client
	// to use when it makes a request.
	// If nil, the client uses http.DefaultTransport.
	Transport func(Context) http.RoundTripper

	// this peer's base URL, e.g. "https://example.net:8000"
	self string //self 必须是一个合法的URL指向当前的服务器，比如 "http://10.0.0.1:8000"

	// opts specifies the options.
	opts HTTPPoolOptions

	mu          sync.Mutex // guards peers and httpGetters
	peers       *consistenthash.Map
	httpGetters map[string]*httpGetter // keyed by e.g. "http://10.0.0.2:8008"
}

// HTTPPoolOptions are the configurations of a HTTPPool.
type HTTPPoolOptions struct {
	// BasePath specifies the HTTP path that will serve groupcache requests.
	// If blank, it defaults to "/_groupcache/".
	BasePath string  // http服务地址前缀，默认为 "/_groupcache/".

	// Replicas specifies the number of key replicas on the consistent hash.
	// If blank, it defaults to 50.
	Replicas int  // 分布式一致性hash中虚拟节点数量，默认 50.

	// HashFn specifies the hash function of the consistent hash.
	// If blank, it defaults to crc32.ChecksumIEEE.
	HashFn consistenthash.Hash    // 分布式一致性hash的hash算法，默认 crc32.ChecksumIEEE.
}

//初始化一个对等节点的HTTPPool,把自己注册成一个对等节点选取器，也把自己注册成p.opts.BasePath路由的处理器。
func NewHTTPPool(self string) *HTTPPool {//参数必须为当前服务器的url,如"http://example.net:8000"
	p := NewHTTPPoolOpts(self, nil) // 初始化HTTPPool，该函数不能重复调用，否则会panic，HTTPPool也是一个http处理器
	//语法：所指定的handle pattern是“/”，则匹配所有的pattern；而“/foo/”则会匹配所有“/foo/*”，golang默认的http处理器是不会检查访问的方法的，无论是get还是post,都可以访问到。
	http.Handle(p.opts.BasePath, p) //这个函数默认会注册一个路由p.opts.BasePath，该路由主要用户节点间获取数据的功能."/_groupcache/",就是说节点向访问就使用这个路由，处理函数就是HTTPPool的ServerHttp函数。
	return p
}

var httpPoolMade bool

// NewHTTPPoolOpts initializes an HTTP pool of peers with the given options.
// Unlike NewHTTPPool, this function does not register the created pool as an HTTP handler.
// The returned *HTTPPool implements http.Handler and must be registered using http.Handle.
func NewHTTPPoolOpts(self string, o *HTTPPoolOptions) *HTTPPool {
	if httpPoolMade { //只调用一次
		panic("groupcache: NewHTTPPool must be called only once")
	}
	httpPoolMade = true

	p := &HTTPPool{
		self:        self, //使用self参数（基础节点的url）初始化一个 HTTPPool对象
		httpGetters: make(map[string]*httpGetter), //在下面的Set中被填充
	}
	if o != nil {
		p.opts = *o
	}
	if p.opts.BasePath == "" {
		p.opts.BasePath = defaultBasePath //在下面Set中会加上类似http://127.0.0.1:8081的前缀
	}
	if p.opts.Replicas == 0 {
		p.opts.Replicas = defaultReplicas //默认复制节点的个数
	}
	p.peers = consistenthash.New(p.opts.Replicas, p.opts.HashFn)  // 根据虚拟节点数量和哈希函数创建一致性哈希节点对象,但是此处并没有创建key或者hashmap，本机节点默认这两个值是0

	RegisterPeerPicker(func() PeerPicker { return p })  // 注册peers.portPicker
	return p
}

// Set updates the pool's list of peers.
// Each peer value should be a valid base URL,
// for example "http://example.net:8000".
func (p *HTTPPool) Set(peers ...string) { // 更新节点列表，用了consistenthash
	p.mu.Lock()
	defer p.mu.Unlock()
	p.peers = consistenthash.New(p.opts.Replicas, p.opts.HashFn)
	p.peers.Add(peers...)
	p.httpGetters = make(map[string]*httpGetter, len(peers))
	for _, peer := range peers {
		p.httpGetters[peer] = &httpGetter{transport: p.Transport, baseURL: peer + p.opts.BasePath} //baseURL就类似为http://127.0.0.1:8081/_groupcache/
	}
}

func (p *HTTPPool) PickPeer(key string) (ProtoGetter, bool) { // 用一致性hash算法选择一个节点，拿服务器节点的。
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.peers.IsEmpty() {
		return nil, false
	}
	if peer := p.peers.Get(key); peer != p.self { //如果拿到的节点地址不是本机的节点地址
		return p.httpGetters[peer], true
	}
	return nil, false
}
// 根据请求的路径获取Group和Key，发送请求并返回结果
//请求历经类似为https://example.net:8000/_groupcache/groupname/key
func (p *HTTPPool) ServeHTTP(w http.ResponseWriter, r *http.Request) { // 用于处理通过HTTP传递过来的grpc请求
	// Parse request.
	if !strings.HasPrefix(r.URL.Path, p.opts.BasePath) { // 判断URL前缀是否合法
		panic("HTTPPool serving unexpected path: " + r.URL.Path)
	}
	parts := strings.SplitN(r.URL.Path[len(p.opts.BasePath):], "/", 2) // 分割URL，并从中提取group和key值，示例请求URL为：https://example.net:8000/_groupcache/groupname/key
	if len(parts) != 2 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	groupName := parts[0]
	key := parts[1]

	// Fetch the value for this group/key.
	group := GetGroup(groupName)  // 根据url中提取的groupname获取group
	if group == nil {
		http.Error(w, "no such group: "+groupName, http.StatusNotFound)
		return
	}
	var ctx Context
	if p.Context != nil {  // 如Context不为空，说明需要使用定制的context
		ctx = p.Context(r)
	}

	group.Stats.ServerRequests.Add(1)
	var value []byte
	err := group.Get(ctx, key, AllocatingByteSliceSink(&value)) // 获取指定key对应的值，也是先从缓存拿，缓存拿不到就从磁盘拿
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Write the value to the response body as a proto message.
	body, err := proto.Marshal(&pb.GetResponse{Value: value}) //序列化响应内容
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf") // 设置http头
	w.Write(body) //设置http  body
}

type httpGetter struct { // 这里实际上实现了Peer模块中的ProtoGetter接口
	transport func(Context) http.RoundTripper
	baseURL   string
}

var bufferPool = sync.Pool{
	New: func() interface{} { return new(bytes.Buffer) },
}
//第二个参数是
// req := &pb.GetRequest{
//		Group: &g.name,
//		Key:   &key,
//	}
func (h *httpGetter) Get(context Context, in *pb.GetRequest, out *pb.GetResponse) error { //该方法根据需要向对等节点查询缓存
	u := fmt.Sprintf(  // 生成请求url，https://example.net:8000/_groupcache/groupname/key，
		"%v%v/%v",
		h.baseURL,
		url.QueryEscape(in.GetGroup()),
		url.QueryEscape(in.GetKey()),
	)
	req, err := http.NewRequest("GET", u, nil)  // 新建Get请求
	if err != nil {
		return err
	}
	tr := http.DefaultTransport //获取transport方法
	if h.transport != nil {
		tr = h.transport(context)
	}
	res, err := tr.RoundTrip(req) // 执行请求
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned: %v", res.Status)
	}
	b := bufferPool.Get().(*bytes.Buffer) // 这里用到了go 提供的 sync.Pool，对字节缓冲数组进行复用，避免了反复申请（缓存期为两次gc之间）
	b.Reset() //字节缓冲重置
	defer bufferPool.Put(b)
	_, err = io.Copy(b, res.Body)  //字节缓冲填充
	if err != nil {
		return fmt.Errorf("reading response body: %v", err)
	}
	err = proto.Unmarshal(b.Bytes(), out) //反序列化字节数组
	if err != nil {
		return fmt.Errorf("decoding response body: %v", err)
	}
	return nil
}
