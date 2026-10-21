//go:build ignore

// The descriptor_set_split tool takes a monolithic descriptor set file and
// separates it into multiple per-file binpb files. This makes it so updates to
// individual files cause git conflicts with one another.
package main

import (
	"fmt"
	"os"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func main() {
	if err := split(); err != nil {
		fmt.Fprintln(os.Stderr, "Error", err)
		os.Exit(1)
	}
}

func split() error {
	data, err := os.ReadFile("descriptor_set.binpb")
	if err != nil {
		return fmt.Errorf("reading descriptor set file: %v", err)
	}
	ds := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(data, ds); err != nil {
		return fmt.Errorf("parsing descriptor set file: %v", err)
	}
	if err := os.MkdirAll("descriptor_set", 0755); err != nil {
		return fmt.Errorf("creating descriptor_set directory")
	}
	r, err := os.OpenRoot("descriptor_set")
	if err != nil {
		return fmt.Errorf("opening descriptor_set directory: %v", err)
	}

	opts := proto.MarshalOptions{
		// Ensure the file doesn't change if generated twice.
		Deterministic: true,
	}

	for _, f := range ds.GetFile() {
		name := strings.TrimSuffix(f.GetName(), ".proto")
		name = strings.ReplaceAll(name, "/", "_") + ".binpb"
		data, err := opts.Marshal(f)
		if err != nil {
			return fmt.Errorf("encoding file: %v", err)
		}
		if err := r.WriteFile(name, data, 0644); err != nil {
			return fmt.Errorf("writing file: %v", err)
		}
	}
	if err := os.Remove("descriptor_set.binpb"); err != nil {
		return fmt.Errorf("removing descriptor set: %v", err)
	}
	return nil
}
