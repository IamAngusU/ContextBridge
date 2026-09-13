package main

import "testing"

func TestChatE2EEToggle(t *testing.T) {
	state := &chatState{}
	if handled, _ := state.command("/e2ee on"); !handled || !state.e2ee {
		t.Fatal("/e2ee on did not enable encryption")
	}
	if handled, _ := state.command("/e2ee off"); !handled || state.e2ee {
		t.Fatal("/e2ee off did not disable encryption")
	}
	if _, message := state.command("/e2ee perhaps"); message == "" || state.e2ee {
		t.Fatal("invalid E2EE value was not rejected")
	}
}
