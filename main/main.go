//运行步骤：
//本人先在工程目录下放上一张帅气的自拍，文件名为big-file.jpg，运行程序后服务器启动，在浏览器输入 http://localhost:8080/thumbnails/big-file.jpg 后，帅气光芒迸发；
//将帅气自拍从本地删除，另起一个server，该server监控8081端口，在浏览器输入 http://localhost:8081/thumbnails/big-file.jpg ，
// 虽然本地并不存在big-file.jpg，但是由于自拍已然缓存，所以帅气光芒依旧迸发；
package main

import (
	"fmt"
	"log"
	"net/http"
	"github.com/golang/groupcache"
	"io/ioutil"
)

func generateThumbnail(fileName string) []byte {
	result, err := ioutil.ReadFile(fileName) //根据文件名路径来读
	if err != nil {
		fmt.Println(err.Error())
		return nil
	}
	return result
}

func main() {
	// me是本缓存服务的地址，peers.Set中是除本地外的其他缓存服务器的地址
	//第一步：根据base peer的地址生成一个HTTPPool，并填充其部分值
	me := "http://127.0.0.1:8080" //注意都要写http://
	peers := groupcache.NewHTTPPool(me)
	//第二步，往生成的HTTPPool中加入其他的节点。
	peers.Set("http://127.0.0.1:8081", "http://127.0.0.1:8082", "http://127.0.0.1:8083")
	// 第三步：创建Group实例.可以有多个group，每个group有不同的获取数据的Getter方法
	var thumbNails = groupcache.NewGroup("thumbnails", 64<<20, groupcache.GetterFunc( //第二个参数是这个group缓冲区的大小，第三个参数 自定义数据获取来源，是唯一能更新缓存的方法
		//在创建Group实例的过程中,传入了一个回调函数,通过这个回调函数,将需要缓存的数据写入到cache中.后边就可以通过Group提供的Get方法,按照key值,获取缓存数据.
		func(ctx groupcache.Context, key string, dest groupcache.Sink) error { //签名一样的函数可以相互转化。
			fileName := key
			dest.SetBytes(generateThumbnail(fileName))
			return nil
		}))
	// 第四步：路由
	http.HandleFunc("/thumbnails/", func(rw http.ResponseWriter, r *http.Request) {
		var data []byte
		//注意下面这个Get方法，远端节点来访问时，在ServerHTTP中调用的就是这个Get方法。
		thumbNails.Get(nil, r.URL.Path[len("/thumbnails/"):], groupcache.AllocatingByteSliceSink(&data))//第二个参数就是文件名
		rw.Write([]byte(data))
	})
	// 启动服务器
	log.Fatal(http.ListenAndServe(me[len("http://"):], nil))
}
