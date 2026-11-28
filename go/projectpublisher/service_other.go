//go:build !linux

package projectpublisher

import "errors"

func Serve() error { return errors.New("project publisher requires Linux peer credentials") }
