package store

import "testing"

// A custom runtime has no adapter to start it, so a definition without a command
// would spawn a Pod that runs its image's default entrypoint and crash-loops
// with nothing explaining why. It has to be refused where the operator can say
// so: at save time.
func TestCustomRuntimeRequiresACommand(t *testing.T) {
	input := CreateAgentInput{RuntimeType: "custom"}
	if err := normaliseCustomRuntime(&input); err == nil {
		t.Fatal("a custom runtime with no command must be rejected")
	}
	input = CreateAgentInput{RuntimeType: "custom", CustomCommand: []string{"  ", ""}}
	if err := normaliseCustomRuntime(&input); err == nil {
		t.Fatal("a command of only blanks is no command at all")
	}
}

func TestCustomRuntimeCommandIsCleaned(t *testing.T) {
	input := CreateAgentInput{RuntimeType: "custom", CustomCommand: []string{" /usr/bin/my-agent ", "", "--port", " 9000 "}, CustomPort: 9000}
	if err := normaliseCustomRuntime(&input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"/usr/bin/my-agent", "--port", "9000"}
	if len(input.CustomCommand) != len(want) {
		t.Fatalf("command = %#v, want %#v", input.CustomCommand, want)
	}
	for i, part := range want {
		if input.CustomCommand[i] != part {
			t.Fatalf("command[%d] = %q, want %q", i, input.CustomCommand[i], part)
		}
	}
}

// The fields belong to the custom runtime alone; carrying them on an adapter
// runtime would put a command in the CRD that the adapter then ignores.
func TestNonCustomRuntimesDropTheCommand(t *testing.T) {
	input := CreateAgentInput{RuntimeType: "opencode", CustomCommand: []string{"anything"}, CustomPort: 1234}
	if err := normaliseCustomRuntime(&input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if input.CustomCommand != nil || input.CustomPort != 0 {
		t.Fatalf("command survived on a non-custom runtime: %#v %d", input.CustomCommand, input.CustomPort)
	}
}

func TestCustomRuntimeRejectsAnImpossiblePort(t *testing.T) {
	input := CreateAgentInput{RuntimeType: "custom", CustomCommand: []string{"serve"}, CustomPort: 70000}
	if err := normaliseCustomRuntime(&input); err == nil {
		t.Fatal("a port outside 1-65535 must be rejected")
	}
}

func TestCustomRuntimeRoundTripsThroughTheSpec(t *testing.T) {
	input := CreateAgentInput{RuntimeType: "custom", SystemPrompt: "prompt", CustomCommand: []string{"serve", "--port", "9000"}, CustomPort: 9000}
	if err := normaliseCustomRuntime(&input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	agent := Agent{RuntimeType: "custom", Spec: agentSpecJSON(input)}
	command, port := agent.CustomRuntime()
	if len(command) != 3 || command[0] != "serve" || port != 9000 {
		t.Fatalf("round trip lost the command: %#v %d", command, port)
	}
	// An adapter runtime must report nothing even if a spec somehow carries one.
	adapter := Agent{RuntimeType: "opencode", Spec: agentSpecJSON(CreateAgentInput{RuntimeType: "custom", CustomCommand: []string{"serve"}, CustomPort: 1})}
	if command, port := adapter.CustomRuntime(); command != nil || port != 0 {
		t.Fatalf("an adapter runtime must not read a custom command: %#v %d", command, port)
	}
}
