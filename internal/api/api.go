// Package api provides utilities for working with the API descriptor set.
package api

import (
	"embed"
	"fmt"
	"path"
	"strings"

	"github.com/oblique-security/terraform-provider-oblique/internal/api/apipb"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// descriptor is the compiled protobuf descriptor set for the API.
// It is used by tools that need access to values that aren't stored in the
// compiled Go types, such as comments.
//
//go:embed descriptor_set/*.binpb
var descriptor embed.FS

// descriptorFiles is used to track comments for messages and fields, which are
// not part of the Go generated types.
var descriptorFiles *protoregistry.Files

func init() {
	opts := protodesc.FileOptions{AllowUnresolvable: true}
	set := &descriptorpb.FileDescriptorSet{}
	ents, err := descriptor.ReadDir("descriptor_set")
	if err != nil {
		panic("reading descriptor sets: " + err.Error())
	}
	for _, e := range ents {
		data, err := descriptor.ReadFile(path.Join("descriptor_set", e.Name()))
		if err != nil {
			panic("reading descriptor set " + e.Name() + ": " + err.Error())
		}
		f := &descriptorpb.FileDescriptorProto{}
		if err := proto.Unmarshal(data, f); err != nil {
			panic("parsing descriptor set " + e.Name() + ": " + err.Error())
		}
		set.File = append(set.File, f)
	}
	files, err := opts.NewFiles(set)
	if err != nil {
		panic("resoling files: " + err.Error())
	}
	descriptorFiles = files
}

// ServiceDescriptor returns the service descriptor for a gRPC service.
func ServiceDescriptor(srv grpc.ServiceDesc) (protoreflect.ServiceDescriptor, error) {
	desc, err := descriptorFiles.FindDescriptorByName(protoreflect.FullName(srv.ServiceName))
	if err != nil {
		return nil, fmt.Errorf("finding service descriptor by name: %s: %v", srv.ServiceName, err)
	}
	sd, ok := desc.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, fmt.Errorf("descriptor is not a service descriptor: %s", srv.ServiceName)
	}
	return sd, nil
}

// MethodAuthorization returns the authz annotations for a method.
func MethodAuthorization(method protoreflect.MethodDescriptor) ([]apipb.Authz, error) {
	opts, ok := method.Options().(*descriptorpb.MethodOptions)
	if !ok {
		return nil, fmt.Errorf("method options are not method options: %s", method.FullName())
	}
	ext, ok := proto.GetExtension(opts, apipb.E_Authz).([]apipb.Authz)
	if !ok {
		return nil, fmt.Errorf("extension is not a slice of apipb.Authz: %s", method.FullName())
	}
	return ext, nil
}

// Comment returns the comment of the type or field.
func Comment(fullName protoreflect.FullName) (string, error) {
	desc, err := descriptorFiles.FindDescriptorByName(fullName)
	if err != nil {
		return "", fmt.Errorf("finding descriptor by name: %s: %v", fullName, err)
	}
	srcs := desc.ParentFile().SourceLocations()
	src := srcs.ByDescriptor(desc)
	return cleanComment(src.LeadingComments), nil
}

// cleanComment processes a proto comment by preserving paragraph breaks,
// collapsing single newlines to spaces, and stripping AIP linter pragma
// comments of the form (-- ... --).
func cleanComment(comment string) string {
	// Strip AIP linter pragma comments: (-- ... --)
	for {
		start := strings.Index(comment, "(--")
		if start == -1 {
			break
		}
		end := strings.Index(comment[start:], "--)")
		if end == -1 {
			break
		}
		comment = comment[:start] + " " + comment[start+end+3:]
	}

	// Split by double-newline to preserve paragraphs.
	paragraphs := strings.Split(comment, "\n\n")
	for i, p := range paragraphs {
		// Within each paragraph, collapse single newlines to spaces.
		paragraphs[i] = strings.Join(strings.Fields(p), " ")
	}

	// Filter out empty paragraphs.
	var result []string
	for _, p := range paragraphs {
		if p != "" {
			result = append(result, p)
		}
	}
	return strings.TrimSpace(strings.Join(result, "\n\n"))
}

// ResourcePatterns returns the patterns for a field that is referenced by a
// resource reference annotation.
//
// For example, passing the field descriptor for the "user" field of the
// GroupEffectiveOwner, which is annotated as:
//
//	(google.api.resource_reference) = {type: "oblique.security.v1/User"};
//
// Would return ["users/{user}"].
func ResourcePatterns(fd protoreflect.FieldDescriptor) []string {
	opts := fd.Options().(*descriptorpb.FieldOptions)
	if opts == nil {
		return nil
	}
	rr, ok := proto.GetExtension(opts, annotations.E_ResourceReference).(*annotations.ResourceReference)
	if !ok {
		return nil
	}
	if rr.GetType() == "" || rr.GetType() == "*" {
		return nil
	}

	var patterns []string
	descriptorFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		msgs := fd.Messages()
		for i := range msgs.Len() {
			msg := msgs.Get(i)
			opts, ok := msg.Options().(*descriptorpb.MessageOptions)
			if !ok {
				continue
			}
			res, ok := proto.GetExtension(opts, annotations.E_Resource).(*annotations.ResourceDescriptor)
			if !ok {
				continue
			}
			if res.GetType() == rr.GetType() {
				patterns = res.GetPattern()
				return false
			}
		}
		return true
	})
	return patterns
}

// FieldBehavior returns the field behaviors for a field.
func FieldBehavior(fd protoreflect.FieldDescriptor) []annotations.FieldBehavior {
	opts := fd.Options().(*descriptorpb.FieldOptions)
	if opts == nil {
		return nil
	}
	behaviors, ok := proto.GetExtension(opts, annotations.E_FieldBehavior).([]annotations.FieldBehavior)
	if !ok {
		return nil
	}
	return behaviors
}
