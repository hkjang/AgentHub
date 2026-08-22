package api

import (
	"os"
	"strings"
	"testing"
)

// Every reference in an exported definition travels by name, because the
// document's purpose is to be imported somewhere else — a review branch, a
// production cluster — where identifiers mean nothing.
//
// Two of the six did not. Security and network profiles were written as raw
// identifiers, on the grounds that the profiles this platform seeds have stable
// ids. They do; a profile an operator creates does not. It gets a fresh uuid,
// which exists in one cluster and nowhere else, so exporting such an agent wrote
// that uuid into the file and importing it elsewhere reached the database with a
// reference to nothing. The person moving a definition between clusters was
// answered with a Postgres foreign key error in English, where every other
// reference answers in a sentence naming what is missing.
func TestEveryReferenceInAnExportTravelsByName(t *testing.T) {
	body, err := os.ReadFile("gitops.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Server) agentToDocument(")
	if at < 0 {
		t.Fatal("agentToDocument is gone; this guard is reading nothing")
	}
	export := source[at:]
	if end := strings.Index(export, "\n}\n"); end >= 0 {
		export = export[:end]
	}
	// Each of the six is written through a name table rather than straight from
	// the agent's column.
	for _, reference := range []struct{ field, table string }{
		{"RuntimeProfile", "names.profiles"},
		{"Workspace", "names.workspaces"},
		{"ModelEndpoint", "names.models"},
		{"MCPBundle", "names.bundles"},
		{"SecurityProfile", "names.security"},
		{"NetworkProfile", "names.network"},
	} {
		line := "document.Spec." + reference.field + " = " + reference.table
		if !strings.Contains(export, line) {
			t.Errorf("%s is not exported by name; the document is only importable into the cluster it came from", reference.field)
		}
	}

	// And every one is resolved on the way back in, so a name this cluster does
	// not have is reported rather than handed to the database.
	at = strings.Index(source, "input := store.CreateAgentInput{")
	if at < 0 {
		t.Fatal("the import mapping is gone; this guard is reading nothing")
	}
	mapping := source[at:]
	if end := strings.Index(mapping, "\n\t}"); end >= 0 {
		mapping = mapping[:end]
	}
	// Whitespace-insensitive: gofmt aligns struct fields, so the number of spaces
	// after the colon depends on the longest field name beside it. An assertion
	// that counts them fails when a field is renamed, which is a guard failing over
	// its own wording rather than over what it guards.
	squeezed := strings.Join(strings.Fields(mapping), " ")
	for _, field := range []string{"RuntimeProfileID", "WorkspaceID", "ModelEndpointID", "MCPBundleID", "SecurityProfileID", "NetworkProfileID"} {
		if !strings.Contains(squeezed, field+": resolve(") {
			t.Errorf("%s is taken from the document without resolving it; an unknown value reaches the database as a foreign key violation", field)
		}
	}

	// A document written before profiles travelled by name carries seeded ids, and
	// must keep importing. The lookup accepts an identifier as well as a name.
	at = strings.Index(source, "func (s *Server) referenceNames(")
	if at < 0 {
		t.Fatal("referenceNames is gone")
	}
	lookup := source[at:]
	if end := strings.Index(lookup, "\n}\n"); end >= 0 {
		lookup = lookup[:end]
	}
	if !strings.Contains(lookup, "byName[strings.ToLower(item.ID)] = item.ID") {
		t.Error("a policy profile's identifier no longer resolves to itself; every document exported before this change stops importing")
	}
}
