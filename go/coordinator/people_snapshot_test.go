package main

import (
	"sync"
	"testing"
)

// R3; risk: people reload races with background director lookup, or returned aliases change authority after publication.
func TestPeopleSnapshotPublicationAndRequestConsistency(t *testing.T) {
	a := &App{}
	if ownerEmail(&Request{app: a}) != "" || ownerEmailOf(a) != "owner" || a.peopleSnapshot().count() != 0 {
		t.Fatal("nil initialization changed")
	}
	person := &Person{Email: "owner@example.test", Name: "old", Owner: true, Roles: []string{"operator"}}
	people := map[string]*Person{person.Email: person}
	a.publishPeople(people)
	old := &Request{app: a}
	if old.peopleSnapshot().person(person.Email).Name != "old" {
		t.Fatal("initial snapshot missing")
	}
	person.Name = "mutated input"
	person.Roles[0] = "architect"
	delete(people, person.Email)
	got := a.peopleSnapshot().person(person.Email)
	if got.Name != "old" || got.Roles[0] != "operator" {
		t.Fatal("publication retained mutable input")
	}
	got.Name = "mutated result"
	got.Roles[0] = "architect"
	list := a.peopleSnapshot().list()
	list[0].Owner = false
	if current := a.peopleSnapshot().person(person.Email); current.Name != "old" || !current.Owner || current.Roles[0] != "operator" {
		t.Fatal("returned mutable snapshot alias")
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 1000; i++ {
			a.publishPeople(map[string]*Person{"owner@example.test": {Email: "owner@example.test", Name: "new", Owner: true}})
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 1000; i++ {
			q := &Request{app: a}
			if director(q) != "owner@example.test" || ownerEmailOf(a) != "owner@example.test" {
				t.Error("director changed during safe publication")
				return
			}
			q.peopleSnapshot().person("owner@example.test").Owner = false
		}
	}()
	close(start)
	wg.Wait()
	if old.peopleSnapshot().person("owner@example.test").Name != "old" || (&Request{app: a}).peopleSnapshot().person("owner@example.test").Name != "new" {
		t.Fatal("request projection not consistent across publication")
	}
	a.publishPeople(nil)
	if a.peopleSnapshot().count() != 0 || old.peopleSnapshot().person("owner@example.test") == nil {
		t.Fatal("nil publication or retained request snapshot changed")
	}
}
