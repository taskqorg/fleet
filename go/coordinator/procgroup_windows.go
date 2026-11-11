package main

import osexec "os/exec"

// ponytail: Windows keeps the default (the shell alone is killed); a job object would cover children if the coordinator ever runs Lab checks there.
func ownGroup(*osexec.Cmd) {}

func killGroup(*osexec.Cmd) {}
