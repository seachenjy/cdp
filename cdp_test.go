package cdp

import (
	"sync"
	"testing"
)

func TestError(t *testing.T) {
	client, err := Init("http://localhost:9222", 5)
	if err != nil {
		t.Error(err)
	}
	urls := []string{
		"http://www.qq.com",
		"http://www.tencent.com",
		"http://www.sina.com",
		"http://www.jandan.net",
		"http://www.weixin.com",
		"http://www.163.com",
		"http://www.taobao.com",
	}
	wait := &sync.WaitGroup{}
	for _, url := range urls {
		wait.Add(1)
		go func(u string, w *sync.WaitGroup) {
			client.Do(func(tab *Tab) {
				tab.Navigate(u)
				title, err := tab.Evaluate("document.title")
				if err != nil {
					t.Error(err)
				}
				w.Done()
				t.Log(title, u)
			})
		}(url, wait)
	}

	wait.Wait()
}
