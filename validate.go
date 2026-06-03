package terraform

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/helpers/validatordiag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/oblique-security/terraform-provider-oblique/internal/api"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

type patternValidator struct {
	patterns []string
}

func (v patternValidator) Description(ctx context.Context) string {
	if len(v.patterns) == 1 {
		return fmt.Sprintf("value must match pattern: %s (segments must match %s)",
			v.patterns[0], resourceIDRegexp)
	}
	return fmt.Sprintf("value must match one of the following patterns: %s (segments must match %s)",
		v.patterns, resourceIDRegexp)
}

func (v patternValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

// https://google.aip.dev/122#resource-id-segments
var resourceIDRegexp = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

func validResourceIDSegement(s string) bool {
	return resourceIDRegexp.MatchString(s)
}

func (v patternValidator) ValidateString(ctx context.Context, request validator.StringRequest, response *validator.StringResponse) {
	if request.ConfigValue.IsNull() || request.ConfigValue.IsUnknown() {
		return
	}

	for _, pattern := range v.patterns {
		if patternMatches(pattern, request.ConfigValue.ValueString()) {
			return
		}
	}

	response.Diagnostics.Append(validatordiag.InvalidAttributeValueMatchDiagnostic(
		request.Path,
		v.Description(ctx),
		request.ConfigValue.String(),
	))
}

// patternMatches validates a resource pattern.
//
// https://github.com/googleapis/googleapis/blob/453b0cc093f83796765503ead7ec9f5f8196125f/google/api/resource.proto#L137-L155
func patternMatches(pattern, val string) bool {
	partsX := strings.Split(pattern, "/")
	partsY := strings.Split(val, "/")
	if len(partsX) != len(partsY) {
		return false
	}
	for i, partX := range partsX {
		partY := partsY[i]
		if !validResourceIDSegement(partY) {
			return false
		}
		if strings.HasPrefix(partX, "{") && strings.HasSuffix(partX, "}") {
			// This is a resource variable. It always matches.
			continue
		}
		if partX != partY {
			return false
		}
	}
	return true
}

// cutLast is like strings.Cut, but splits on the last occurrence of the
// separator rather than the first.
func cutLast(s string, sep string) (before, after string, ok bool) {
	i := strings.LastIndex(s, sep)
	if i == -1 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

// cutLast2 performs cutLast twice.
func cutLast2(s string, sep string) (before, after string, ok bool) {
	if before, after, ok = cutLast(s, sep); !ok {
		return
	}
	return cutLast(before, sep)
}

type identifier struct {
	nameValidator   validator.String
	parentValidator validator.String
	namePrefix      string
	isSingleton     bool
}

func parseIdentifier(fd protoreflect.FieldDescriptor) (*identifier, bool) {
	opts := fd.Options().(*descriptorpb.FieldOptions)
	if opts == nil {
		return nil, false
	}
	behaviors, ok := proto.GetExtension(opts, annotations.E_FieldBehavior).([]annotations.FieldBehavior)
	if !ok {
		return nil, false
	}
	var isIdent bool
	if slices.Contains(behaviors, annotations.FieldBehavior_IDENTIFIER) {
		isIdent = true
	}
	if !isIdent {
		return nil, false
	}
	msg := fd.ContainingMessage()
	if msg == nil {
		return nil, false
	}
	mo, ok := msg.Options().(*descriptorpb.MessageOptions)
	if !ok {
		return nil, false
	}
	res, ok := proto.GetExtension(mo, annotations.E_Resource).(*annotations.ResourceDescriptor)
	if !ok {
		return nil, false
	}
	if res.GetType() == "" {
		return nil, false
	}
	if len(res.GetPattern()) != 1 {
		return nil, false
	}
	pattern := res.GetPattern()[0]
	ident := &identifier{
		nameValidator: &patternValidator{
			patterns: []string{pattern},
		},
		isSingleton: strings.Count(pattern, "/")%2 == 0,
	}

	if ident.isSingleton {
		// The resource is a singleton. ("teams/{team}/roles").
		if parent, rest, ok := cutLast(pattern, "/"); ok {
			ident.parentValidator = &patternValidator{
				patterns: []string{parent},
			}
			ident.namePrefix = rest
		} else {
			// If no parent, then then singleton is the prefix ("config").
			ident.namePrefix = pattern
		}
	} else {
		// The resource isn't a singleton. ("teams/{team}/members/{member}").
		if parent, rest, ok := cutLast2(pattern, "/"); ok {
			ident.parentValidator = &patternValidator{
				patterns: []string{parent},
			}
			prefix, _, _ := strings.Cut(rest, "/")
			ident.namePrefix = prefix
		} else {
			// If no parent than the first element of the pattern is the prefix ("teams/{team}").
			prefix, _, _ := strings.Cut(pattern, "/")
			ident.namePrefix = prefix
		}
	}
	return ident, true
}

func parseResourceReference(fd protoreflect.FieldDescriptor) (validator.String, bool) {
	patterns := api.ResourcePatterns(fd)
	if len(patterns) > 0 {
		return patternValidator{patterns: patterns}, true
	}
	return nil, false
}
