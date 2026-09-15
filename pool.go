package tdx

import (
	"errors"
	"strings"
	"time"

	"github.com/injoyai/base/safe"
	"github.com/injoyai/logs"
)

// NewPool 简易版本的连接池
func NewPool(dial func() (*Client, error), number int) (*Pool, error) {
	if number <= 0 {
		number = 1
	}
	ch := make(chan *Client, number)
	p := &Pool{
		ch: ch,
		Closer: safe.NewCloser().SetCloseFunc(func(err error) error {
			close(ch)
			return nil
		}),
	}
	for i := 0; i < number; i++ {
		c, err := dial()
		if err != nil {
			return nil, err
		}
		p.ch <- c
	}
	return p, nil
}

type Pool struct {
	ch chan *Client
	*safe.Closer
}

func (this *Pool) Get() (*Client, error) {
	select {
	case <-this.Done():
		return nil, this.Err()
	case c, ok := <-this.ch:
		if !ok {
			return nil, errors.New("已关闭")
		}
		return c, nil
	}
}

func (this *Pool) Put(c *Client) {
	select {
	case <-this.Done():
		c.Close()
		return
	case this.ch <- c:
	}
}

func (this *Pool) Do(fn func(c *Client) error) error {
	c, err := this.Get()
	if err != nil {
		return err
	}
	defer this.Put(c)
	if err := fn(c); err == nil {
		return nil
	} else if !isNetErr(err) {
		return err //业务错误（错码/解码失败等）不重试
	}
	//网络类错误：等底层重拨完成再试一次（重拨一般1~2秒）
	logs.Err(err, "连接异常，3秒后重试一次...")
	<-time.After(time.Second * 3)
	return fn(c)
}

// isNetErr 是否网络类错误（值得等重拨后重试一次）
func isNetErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, sub := range []string{
		"timeout", "deadline", "eof", "reset", "broken pipe",
		"refused", "network", "connection", "closed", "unreachable",
	} {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func (this *Pool) Go(fn func(c *Client)) error {
	c, err := this.Get()
	if err != nil {
		return err
	}
	go func(c *Client) {
		defer this.Put(c)
		fn(c)
	}(c)
	return nil
}
