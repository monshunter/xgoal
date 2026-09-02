package service

import "example.com/xgoal/benchmark/lib"

func Greeting(name string) string { return lib.Message(name) + "!" }
