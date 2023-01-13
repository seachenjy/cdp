package cdp

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestError(t *testing.T) {
	client, err := Init("http://localhost:9222", 5)
	if err != nil {
		t.Error(err)
	}
	urls := []string{
		"http://www.baidu.com",
		"http://www.163.com",
		"http://www.qq.com",
		"http://www.tencent.com",
		"http://www.sina.com",
		"http://www.jandan.net",
		"http://www.weixin.com",
	}
	wait := &sync.WaitGroup{}
	for _, url := range urls {
		wait.Add(1)
		go func(url string, wait *sync.WaitGroup) {
			client.Do(func(tab *Tab) {
				tab.Navigate(url)
				time.Sleep(5 * time.Second)
				title, err := tab.Evalate("document.title", time.Second*5)
				fmt.Println(title, err, url)
				wait.Done()
			})
		}(url, wait)
	}
	wait.Wait()
}
