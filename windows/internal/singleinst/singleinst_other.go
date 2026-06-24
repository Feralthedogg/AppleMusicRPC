//go:build !windows && !darwin

package singleinst

type Lock struct{}

func (l *Lock) Release() {}

func Acquire(_ string) (*Lock, bool, error) {
	return &Lock{}, false, nil
}
