package pool_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type Counter struct {
	mutex sync.Mutex
	n     int
}

// Next increments the counter and returns the value
func (c *Counter) Next() int {
	c.mutex.Lock()
	c.n += 1
	n := c.n
	c.mutex.Unlock()
	return n
}

// Value returns the counter
func (c *Counter) Value() int {
	c.mutex.Lock()
	n := c.n
	c.mutex.Unlock()
	return n
}

func stubCloseRes(interface{}) error { return nil }

func TestPoolGetCreatesResourceWhenNoneAvailable(t *testing.T) {
	var createCalls Counter
	createFunc := func() (interface{}, error) {
		return createCalls.Next(), nil
	}
	pool := pool.New(createFunc, stubCloseRes)

	res, err := pool.Get(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, res)

	pool.Return(res)
}

func TestPoolGetDoesNotCreatesResourceWhenItWouldExceedMaxSize(t *testing.T) {
	var createCalls Counter
	createFunc := func() (interface{}, error) {
		return createCalls.Next(), nil
	}
	pool := pool.New(createFunc, stubCloseRes)
	pool.SetMaxSize(1)

	wg := &sync.WaitGroup{}

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			for j := 0; j < 100; j++ {
				res, err := pool.Get(context.Background())
				assert.NoError(t, err)
				assert.Equal(t, 1, res)
				pool.Return(res)
			}
			wg.Done()
		}()
	}

	wg.Wait()

	assert.Equal(t, 1, createCalls.Value())
	assert.Equal(t, 1, pool.Size())
}

func TestPoolGetReturnsErrorFromFailedResourceCreate(t *testing.T) {
	errCreateFailed := errors.New("create failed")
	createFunc := func() (interface{}, error) {
		return nil, errCreateFailed
	}
	pool := pool.New(createFunc, stubCloseRes)

	res, err := pool.Get(context.Background())
	assert.Equal(t, errCreateFailed, err)
	assert.Nil(t, res)
}

func TestPoolGetReusesResources(t *testing.T) {
	var createCalls Counter
	createFunc := func() (interface{}, error) {
		return createCalls.Next(), nil
	}
	pool := pool.New(createFunc, stubCloseRes)

	res, err := pool.Get(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, res)

	pool.Return(res)

	res, err = pool.Get(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, res)

	pool.Return(res)

	assert.Equal(t, 1, createCalls.Value())
}

func TestPoolGetContextAlreadyCanceled(t *testing.T) {
	createFunc := func() (interface{}, error) {
		panic("should never be called")
	}
	pool := pool.New(createFunc, stubCloseRes)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := pool.Get(ctx)
	assert.Equal(t, context.Canceled, err)
	assert.Nil(t, res)
}

func TestPoolGetContextCanceledDuringCreate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var createCalls Counter
	createFunc := func() (interface{}, error) {
		cancel()
		time.Sleep(1 * time.Second)
		return createCalls.Next(), nil
	}
	pool := pool.New(createFunc, stubCloseRes)

	res, err := pool.Get(ctx)
	assert.Equal(t, context.Canceled, err)
	assert.Nil(t, res)
}

func TestPoolReturnPanicsIfResourceNotPartOfPool(t *testing.T) {
	var createCalls Counter
	createFunc := func() (interface{}, error) {
		return createCalls.Next(), nil
	}
	pool := pool.New(createFunc, stubCloseRes)

	assert.Panics(t, func() { pool.Return(42) })
}

func TestPoolCloseClosesAllAvailableResources(t *testing.T) {
	var createCalls Counter
	createFunc := func() (interface{}, error) {
		return createCalls.Next(), nil
	}

	var closeCalls Counter
	closeFunc := func(interface{}) error {
		closeCalls.Next()
		return nil
	}

	p := pool.New(createFunc, closeFunc)

	resources := make([]interface{}, 4)
	for i := range resources {
		var err error
		resources[i], err = p.Get(context.Background())
		require.Nil(t, err)
	}

	for _, res := range resources {
		p.Return(res)
	}

	p.Close()

	assert.Equal(t, len(resources), closeCalls.Value())
}

func TestPoolReturnClosesResourcePoolIsAlreadyClosed(t *testing.T) {
	var createCalls Counter
	createFunc := func() (interface{}, error) {
		return createCalls.Next(), nil
	}

	var closeCalls Counter
	closeFunc := func(interface{}) error {
		closeCalls.Next()
		return nil
	}

	p := pool.New(createFunc, closeFunc)

	resources := make([]interface{}, 4)
	for i := range resources {
		var err error
		resources[i], err = p.Get(context.Background())
		require.Nil(t, err)
	}

	p.Close()
	assert.Equal(t, 0, closeCalls.Value())

	for _, res := range resources {
		p.Return(res)
	}

	assert.Equal(t, len(resources), closeCalls.Value())
}

func TestPoolGetReturnsErrorWhenPoolIsClosed(t *testing.T) {
	var createCalls Counter
	createFunc := func() (interface{}, error) {
		return createCalls.Next(), nil
	}
	p := pool.New(createFunc, stubCloseRes)
	p.Close()

	res, err := p.Get(context.Background())
	assert.Equal(t, pool.ErrClosedPool, err)
	assert.Nil(t, res)
}

func BenchmarkPoolGetAndReturnNoContention(b *testing.B) {
	var createCalls Counter
	createFunc := func() (interface{}, error) {
		return createCalls.Next(), nil
	}
	pool := pool.New(createFunc, stubCloseRes)

	for i := 0; i < b.N; i++ {
		res, err := pool.Get(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		pool.Return(res)
	}
}

func BenchmarkPoolGetAndReturnHeavyContention(b *testing.B) {
	poolSize := 8
	contentionClients := 15

	var createCalls Counter
	createFunc := func() (interface{}, error) {
		return createCalls.Next(), nil
	}
	pool := pool.New(createFunc, stubCloseRes)
	pool.SetMaxSize(poolSize)

	doneChan := make(chan struct{})
	defer close(doneChan)
	for i := 0; i < contentionClients; i++ {
		go func() {
			for {
				select {
				case <-doneChan:
					return
				default:
				}

				res, err := pool.Get(context.Background())
				if err != nil {
					b.Fatal(err)
				}
				pool.Return(res)
			}
		}()
	}

	for i := 0; i < b.N; i++ {
		res, err := pool.Get(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		pool.Return(res)
	}
}