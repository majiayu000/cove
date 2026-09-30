package app

import (
	"github.com/tiktoken-go/tokenizer"
	"sync"
)

// The encoding is a disclosed wire estimate, never a model billing assertion.
var tokenEstimator struct {
	once  sync.Once
	mu    sync.Mutex
	codec tokenizer.Codec
	err   error
}

func estimateTokens(text string) (int64, error) {
	tokenEstimator.once.Do(func() { tokenEstimator.codec, tokenEstimator.err = tokenizer.Get(tokenizer.O200kBase) })
	if tokenEstimator.err != nil {
		return 0, tokenEstimator.err
	}
	tokenEstimator.mu.Lock()
	defer tokenEstimator.mu.Unlock()
	n, err := tokenEstimator.codec.Count(text)
	return int64(n), err
}
