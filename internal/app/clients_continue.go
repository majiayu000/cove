package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"gopkg.in/yaml.v3"
)

// Continue models are selected by their unique name, not their list index.
// Only the Cove item is encoded; other models and top-level bytes stay intact.
func parseContinueDocument(d *clientDocument, b []byte, exists bool) (*clientDocument, error) {
	if !exists {
		b = []byte("name: Cove local\nversion: 1.0.0\nschema: v1\n")
	}
	root, _, err := extensionYAML(b)
	if err != nil {
		return nil, err
	}
	if err = root.Decode(&d.values); err != nil {
		return nil, err
	}
	for _, name := range []string{"name", "version", "schema"} {
		value, ok := d.values[name].(string)
		if !ok || value == "" || name == "schema" && value != "v1" {
			return nil, errors.New("Continue config.yaml requires name, version and schema: v1")
		}
	}
	_, seq := continueModels(root)
	models := map[string]any{}
	if seq != nil {
		if seq.Kind != yaml.SequenceNode || seq.Style&yaml.FlowStyle != 0 {
			return nil, errors.New("unsupported_format: Continue models must use a block list")
		}
		for _, item := range seq.Content {
			var value map[string]any
			if item.Kind != yaml.MappingNode || item.Style&yaml.FlowStyle != 0 || item.Decode(&value) != nil {
				return nil, errors.New("unsupported_format: model must be a block mapping")
			}
			name, ok := value["name"].(string)
			if !ok || name == "" || models[name] != nil {
				return nil, errors.New("Continue models require unique explicit names; unresolved uses are not edited")
			}
			models[name] = value
		}
		d.values["models"] = models
	}
	d.raw = append([]byte(nil), b...)
	return d, nil
}

func continueModels(root *yaml.Node) (*yaml.Node, *yaml.Node) {
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "models" {
			return root.Content[i], root.Content[i+1]
		}
	}
	return nil, nil
}

func (d *clientDocument) editContinue(fields []clientField, prune [][]string) ([]byte, error) {
	if len(fields) == 0 {
		return append([]byte(nil), d.raw...), nil
	}
	root, _, err := extensionYAML(d.raw)
	if err != nil {
		return nil, err
	}
	key, seq := continueModels(root)
	var item *yaml.Node
	itemIndex := -1
	if seq != nil {
		for i, candidate := range seq.Content {
			var value map[string]any
			if err = candidate.Decode(&value); err != nil {
				return nil, err
			}
			if value["name"] == "Cove" {
				item, itemIndex = candidate, i
				break
			}
		}
	}
	if item == nil {
		item = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	for _, field := range fields {
		if len(field.Path) < 3 || field.Path[0] != "models" || field.Path[1] != "Cove" {
			return nil, errors.New("unsupported Continue field")
		}
		parent := item
		for _, part := range field.Path[2 : len(field.Path)-1] {
			var next *yaml.Node
			for i := 0; i < len(parent.Content); i += 2 {
				if parent.Content[i].Value == part {
					next = parent.Content[i+1]
					break
				}
			}
			if next == nil {
				next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: part}, next)
			}
			if next.Kind != yaml.MappingNode {
				return nil, errors.New("unsupported Continue requestOptions structure")
			}
			parent = next
		}
		name := field.Path[len(field.Path)-1]
		index := -1
		for i := 0; i < len(parent.Content); i += 2 {
			if parent.Content[i].Value == name {
				index = i
				break
			}
		}
		if !field.OursPresent {
			if index >= 0 {
				// Keep comments when a field owned by this change is removed.
				for _, node := range parent.Content[index : index+2] {
					for _, comment := range []string{node.HeadComment, node.LineComment, node.FootComment} {
						if comment != "" {
							parent.FootComment += "\n" + comment
						}
					}
				}
				parent.Content = append(parent.Content[:index], parent.Content[index+2:]...)
			}
			continue
		}
		value := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: field.Ours}
		if name == "roles" || name == "useResponsesApi" || len(field.Path) == 5 && field.Path[2] == "requestOptions" && field.Path[3] == "extraBodyProperties" {
			var typed any
			if err = json.Unmarshal([]byte(field.Ours), &typed); err != nil {
				return nil, err
			}
			if err = value.Encode(typed); err != nil {
				return nil, err
			}
		}
		if index < 0 {
			parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, value)
		} else {
			old := parent.Content[index+1]
			value.HeadComment, value.LineComment, value.FootComment = old.HeadComment, old.LineComment, old.FootComment
			value.Style = old.Style
			parent.Content[index+1] = value
		}
	}
	// Remove only newly created empty nested maps; preserve user options.
	for i := len(prune) - 1; i >= 0; i-- {
		path := prune[i]
		if len(path) <= 2 || path[0] != "models" || path[1] != "Cove" {
			continue
		}
		parent := item
		for depth, part := range path[2:] {
			index := -1
			for at := 0; at < len(parent.Content); at += 2 {
				if parent.Content[at].Value == part {
					index = at
					break
				}
			}
			if index < 0 {
				break
			}
			next := parent.Content[index+1]
			if depth == len(path)-3 && next.Kind == yaml.MappingNode && len(next.Content) == 0 {
				parent.Content = append(parent.Content[:index], parent.Content[index+2:]...)
				break
			}
			parent = next
		}
	}
	offsets := extensionLineOffsets(d.raw)
	startOf := func(node *yaml.Node) int {
		line := node.Line - 1
		comments := 0
		if node.HeadComment != "" {
			comments = len(strings.Split(node.HeadComment, "\n"))
		}
		for line > 0 && comments > 0 {
			previous := bytes.TrimSpace(d.raw[offsets[line-1]:offsets[line]])
			if len(previous) > 0 && !bytes.HasPrefix(previous, []byte("#")) {
				break
			}
			line--
			if len(previous) > 0 {
				comments--
			}
		}
		return offsets[line]
	}
	end := len(d.raw)
	if key != nil {
		for i := 0; i+2 < len(root.Content); i += 2 {
			if root.Content[i] == key {
				end = startOf(root.Content[i+2])
				break
			}
		}
	}
	start := end
	if itemIndex >= 0 {
		start = startOf(seq.Content[itemIndex])
		if itemIndex+1 < len(seq.Content) {
			end = startOf(seq.Content[itemIndex+1])
		}
	}
	var entry []byte
	if len(item.Content) > 0 {
		var encoded bytes.Buffer
		encoder := yaml.NewEncoder(&encoded)
		encoder.SetIndent(2)
		if err = errors.Join(encoder.Encode(&yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{item}}), encoder.Close()); err != nil {
			return nil, err
		}
		indent := 2
		if seq != nil && len(seq.Content) > 0 {
			indent = seq.Content[0].Column - 3 // Mapping starts after the list dash.
		}
		for _, line := range bytes.SplitAfter(encoded.Bytes(), []byte("\n")) {
			if len(line) > 0 {
				entry = append(entry, []byte(strings.Repeat(" ", max(0, indent)))...)
				entry = append(entry, line...)
			}
		}
	} else if itemIndex >= 0 {
		for _, comment := range []string{item.HeadComment, item.FootComment} {
			if comment != "" {
				entry = append(entry, []byte(comment+"\n")...)
			}
		}
	}
	if key == nil && len(entry) > 0 {
		entry = append([]byte("models:\n"), entry...)
	}
	if start == end && len(entry) > 0 && start > 0 && d.raw[start-1] != '\n' {
		entry = append([]byte("\n"), entry...)
	}
	out := append(append(append([]byte{}, d.raw[:start]...), entry...), d.raw[end:]...)
	if len(item.Content) == 0 && seq != nil && len(seq.Content) == 1 {
		for _, parent := range prune {
			if len(parent) == 1 && parent[0] == "models" {
				keyStart, keyEnd := offsets[key.Line-1], offsets[key.Line]
				out = append(out[:keyStart], out[keyEnd:]...)
				break
			}
		}
	}
	if _, err = parseClientDocument("continue", d.path, out, true); err != nil {
		return nil, err
	}
	return out, nil
}
