// Simple groupcache example: https://github.com/golang/groupcache
// Running 3 instances:
// go run main.go -addr=:8080 -pool=http://127.0.0.1:8080,http://127.0.0.1:8081,http://127.0.0.1:8082
// go run main.go -addr=:8081 -pool=http://127.0.0.1:8081,http://127.0.0.1:8080,http://127.0.0.1:8082
// go run main.go -addr=:8082 -pool=http://127.0.0.1:8082,http://127.0.0.1:8080,http://127.0.0.1:8081
// Testing:
// curl localhost:8080/color?name=red
package main

import (
	"errors"
	"flag"
	"log"
	"net/http"
	"strings"
	"github.com/golang/groupcache"
)

var Store = map[string][]byte{
	"red":   []byte("#FF0000"),
	"green": []byte("#00FF00"),
	"blue":  []byte("#0000FF"),
}
//创建分组，一个group是一个存储模块，类似命名空间，可以创建多个，指定了groupcache的名称，大小，以及获取源数据的方法，当客户端程序需要获取某个值时，如果调用了Group.Get函数，就从这个Group集群中获取.
//第二个是这个分组在内存中最大占用空间
//第三个就是Getter接口类型变量了.这个变量很关键,每一个不在groupcache中的key,
//都会触发这个Getter接口类型变量的Get方法.这个Get方法如果不将key对应的value存入到groupcache中,
// 则下次用户再次查询key的缓存时,任然会触发Getter接口类型变量的Get方法,
// 如果在Getter接口变量的Get方法中不去存储key的value到groupcache的lru中,那么整个groupcache将失去意义.并且将会浪费资源,降低系统性能.
//也就是说当groupcache以及peer不存在所需数据时，用户可以自己定义从哪获取数据以及如何获取数据（比如文件或者数据库），即定义Getter的实例即可；
//groupcache的特点:
//1.按分组来存储管理缓存.
//2.每一个组提供了一个写入key的value到cache的处理函数,即Getter变量的Get方法,就算key值不同,但是处理函数确都一样
//3.没有提供delete group操作.
var Group = groupcache.NewGroup("foobar", 64<<20, groupcache.GetterFunc( //第三个参数 自定义数据获取来源
	//在创建Group实例的过程中,传入了一个回调函数,通过这个回到函数,将需要缓存的数据写入到cache中.后边就可以通过Group提供的Get方法,按照key值,获取缓存数据.
	func(ctx groupcache.Context, key string, dest groupcache.Sink) error {
		log.Println("looking up", key)
		v, ok := Store[key]
		if !ok {
			return errors.New("color not found")
		}
		dest.SetBytes(v)
		return nil
	},
))

func main() {
	addr := flag.String("addr", ":8080", "server address")
	peers := flag.String("pool", "http://localhost:8080", "server pool list")
	flag.Parse()
	http.HandleFunc("/color", func(w http.ResponseWriter, r *http.Request) {
		color := r.FormValue("name")
		var b []byte
		err := Group.Get(nil, color, groupcache.AllocatingByteSliceSink(&b))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Write(b)
		w.Write([]byte{'\n'})
	})
	p := strings.Split(*peers, ",")
	pool := groupcache.NewHTTPPool(p[0])
	pool.Set(p...)
	http.ListenAndServe(*addr, nil)
}