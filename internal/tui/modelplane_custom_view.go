package tui

import (
	"fmt"
	"strings"
)

// renderCustomPane draws the right-hand Custom surface. It deliberately uses
// the same list, divider, and detail vocabulary as the provider/model panes:
// Custom is another model source, not a settings screen.
func renderCustomPane(plane *ModelPlaneState, pal *Palette, state State, layout Layout, width, contentH int) []string {
	switch plane.CustomView {
	case CustomViewEditor:
		return renderCustomEditorPane(plane, pal, width)
	case CustomViewDiscovery:
		return renderCustomDiscoveryPane(plane, pal, width, contentH)
	case CustomViewEndpoint:
		return renderCustomEndpointPane(plane, pal, width, contentH)
	default:
		return renderCustomEndpointsPane(plane, pal, width)
	}
}

// renderCustomEndpointsPane lists configured endpoints with their model count.
func renderCustomEndpointsPane(plane *ModelPlaneState, pal *Palette, width int) []string {
	heading := "MODEL · custom"
	lines := []string{pal.Title.Render(TruncateToWidth(heading, width))}
	lines = append(lines, pal.Muted.Render(TruncateToWidth("  Custom OpenAI-compatible endpoints", width)))

	if len(plane.Endpoints) == 0 {
		lines = append(lines, "")
		lines = append(lines, pal.Muted.Render(TruncateToWidth("  No endpoints configured.", width)))
		lines = append(lines, "")
		lines = append(lines, TruncateToWidth("  "+pal.Bold.Render("[Enter]")+pal.Muted.Render(" Add endpoint"), width))
		return lines
	}

	rows := plane.VisibleRows
	if rows <= 0 {
		rows = len(plane.Endpoints)
	}
	window, start := window(plane.Endpoints, plane.EndpointOffset, rows)
	for i, endpoint := range window {
		marker := "  "
		if start+i == plane.EndpointCursor && plane.Focus == PlaneFocusModels {
			marker = pal.Selected.Render("> ")
		}
		name := sanitizeInline(endpoint.DisplayName())
		count := formatCount(len(endpoint.Models))
		line := marker + name
		if room := width - CellWidth(line) - CellWidth(count) - 2; room >= 1 {
			line += strings.Repeat(" ", room) + pal.Muted.Render(count)
		}
		lines = append(lines, TruncateToWidth(line, width))
	}
	if hidden := len(plane.Endpoints) - start - len(window); hidden > 0 {
		lines = append(lines, pal.Muted.Render(TruncateToWidth(fmt.Sprintf("  … %d more", hidden), width)))
	}
	models := 0
	for _, endpoint := range plane.Endpoints {
		models += len(endpoint.Models)
	}
	lines = append(lines, pal.Muted.Render(TruncateToWidth(strings.Repeat("─", maxInt(width, 1)), width)))
	lines = append(lines, TruncateToWidth("  "+pal.Muted.Render(fmt.Sprintf("%d %s · %d models",
		len(plane.Endpoints), plural(len(plane.Endpoints), "endpoint", "endpoints"), models)), width))
	return lines
}

// plural picks the singular or plural noun for a count.
func plural(count int, singular, many string) string {
	if count == 1 {
		return singular
	}
	return many
}

// renderCustomEndpointPane draws one endpoint's models and concise metadata.
// The model rows and detail are the shared renderer; only the endpoint block is
// custom-specific.
func renderCustomEndpointPane(plane *ModelPlaneState, pal *Palette, width, contentH int) []string {
	endpoint, ok := plane.customEndpointModel()
	if !ok {
		return []string{pal.Muted.Render(TruncateToWidth("  endpoint no longer configured", width))}
	}
	heading := "MODEL · " + sanitizeInline(endpoint.DisplayName())
	if plane.Focus == PlaneFocusModels {
		heading = "▶ " + heading
	}
	lines := []string{pal.Title.Render(TruncateToWidth(heading, width))}

	models := plane.customModelOptions()
	if len(models) == 0 {
		lines = append(lines, pal.Muted.Render(TruncateToWidth("  no models configured", width)))
		lines = append(lines, pal.Muted.Render(TruncateToWidth("  press e to edit or p to probe", width)))
	} else {
		rows := plane.VisibleRows
		if rows <= 0 || rows > len(models) {
			rows = len(models)
		}
		if contentH > 0 {
			// Reserve the endpoint block and the model detail beneath the
			// list; a long model roster must not truncate the evidence that
			// explains what the list belongs to.
			if maxRows := contentH - 9; maxRows >= 1 && rows > maxRows {
				rows = maxRows
			}
		}
		window, start := window(models, plane.ModelOffset, rows)
		for i, model := range window {
			lines = append(lines, renderPlaneModelRow(start+i, plane, model, pal, width))
		}
		if hidden := len(models) - start - len(window); hidden > 0 {
			lines = append(lines, pal.Muted.Render(TruncateToWidth(fmt.Sprintf("  … %d more", hidden), width)))
		}
	}

	lines = append(lines, pal.Muted.Render(TruncateToWidth(strings.Repeat("─", maxInt(width, 1)), width)))
	lines = append(lines, pal.Muted.Render(TruncateToWidth("Endpoint", width)))
	add := func(label, value string) {
		if value == "" {
			value = "—"
		}
		lines = append(lines, TruncateToWidth("  "+pal.Muted.Render(padLabel(label))+sanitizeInline(value), width))
	}
	add("URL", endpoint.BaseURL)
	add("Protocol", "OpenAI-compatible")
	keyEnv := endpoint.APIKeyEnv
	if keyEnv == "" {
		keyEnv = "none (keyless)"
	}
	add("Key", keyEnv)
	add("Status", customEndpointStatus(plane, endpoint))
	if endpoint.ContextWindow > 0 {
		facts := formatCount(endpoint.ContextWindow) + " ctx / " + formatCount(endpoint.MaxTokens) + " out"
		if endpoint.FactsAutomatic {
			facts += " (automatic)"
		}
		add("Facts", facts)
	}

	if contentH > 0 {
		budget := contentH - len(lines) - 1
		if budget > 0 {
			if model, ok := plane.ChosenModel(); ok {
				lines = append(lines, planeModelDetailFor(plane, model, pal, width, budget)...)
			}
		}
	}
	return lines
}

// customEndpointStatus reports probe evidence without collapsing configured,
// reachable, and authenticated into one claim.
func customEndpointStatus(plane *ModelPlaneState, endpoint EndpointOption) string {
	if plane.EndpointProbing {
		return "probing…"
	}
	result := plane.EndpointProbe
	if result.Detail != "" {
		protocol := result.Protocol || result.Models > 0 || len(result.ModelIDs) > 0
		switch {
		case result.Reachable && result.Authenticated && protocol:
			return "✓ reachable · OpenAI-compatible" + discoveredSuffix(result.Models)
		case result.Reachable && result.Authenticated:
			return "reachable · credential accepted · model list unreadable"
		case result.Reachable:
			return "reachable · credential rejected"
		default:
			return "not reachable"
		}
	}
	if endpoint.BaseURL == "" {
		return "not configured"
	}
	return "not probed"
}

// discoveredSuffix names discovered models without implying they are saved.
func discoveredSuffix(count int) string {
	if count <= 0 {
		return ""
	}
	return fmt.Sprintf(" · %d discovered", count)
}

// renderCustomEditorPane draws the compact three-field form. Only the focused
// field shows a cursor; the protocol is stated as fixed because it is fixed.
func renderCustomEditorPane(plane *ModelPlaneState, pal *Palette, width int) []string {
	editor := plane.EndpointEditor
	if editor == nil {
		return nil
	}
	lines := []string{pal.Title.Render(TruncateToWidth("CUSTOM ENDPOINT", width))}
	if editor.Key != "" {
		lines = append(lines, TruncateToWidth("  "+pal.Muted.Render(padLabel("Reference"))+sanitizeInline(editor.Key), width))
	}
	lines = append(lines, "")
	labels := []string{"Name", "Base URL", "API key environment"}
	for field := range customEditorFields {
		selected := field == editor.Field
		label := labels[field]
		if selected {
			label = "▶ " + label
		}
		lines = append(lines, TruncateToWidth("  "+pal.Muted.Render(label), width))
		lines = append(lines, renderEditorValue(editor, field, selected, pal, width))
		lines = append(lines, "")
	}
	lines = append(lines, TruncateToWidth("  "+pal.Muted.Render("Protocol")+"  OpenAI-compatible", width))
	lines = append(lines, TruncateToWidth("  "+pal.Muted.Render("Models")+"    "+fmt.Sprintf("%d configured", len(editor.Models)), width))

	lines = append(lines, pal.Muted.Render(TruncateToWidth(strings.Repeat("─", maxInt(width, 1)), width)))
	lines = append(lines, pal.Muted.Render(TruncateToWidth("Status", width)))
	status := editor.ProbeStatus
	if status == "" {
		status = "not probed"
	}
	if editor.Probing {
		status = "probing…"
	}
	lines = append(lines, TruncateToWidth("  "+sanitizeInline(status), width))
	if len(editor.Discovered) > 0 && !editor.Probing {
		lines = append(lines, TruncateToWidth("  "+pal.Muted.Render(fmt.Sprintf("%d discovered · m selects models",
			len(editor.Discovered))), width))
	}
	return lines
}

// renderEditorValue draws one inline field with its cursor when focused.
func renderEditorValue(editor *CustomEndpointEditor, field int, focused bool, pal *Palette, width int) string {
	value := []rune(*editor.fieldValue(field))
	cursor := clampIndex(*editor.fieldCursor(field), len(value)+1)
	if !focused {
		text := string(value)
		if text == "" {
			text = pal.Muted.Render("—")
		}
		return TruncateToWidth("  > "+text, width)
	}
	shown := string(value[:cursor]) + pal.Cursor.Render(" ") + string(value[cursor:])
	return TruncateToWidth(pal.Muted.Render("  > ")+shown, width)
}

// renderCustomDiscoveryPane shows discovered and configured models as an
// explicit selection. It never claims a selected row is persisted.
func renderCustomDiscoveryPane(plane *ModelPlaneState, pal *Palette, width, contentH int) []string {
	discovery := plane.Discovery
	lines := []string{pal.Title.Render(TruncateToWidth("DISCOVERED MODELS", width))}
	lines = append(lines, pal.Muted.Render(TruncateToWidth("  runtime evidence — a saves the selection", width)))
	if discovery == nil || len(discovery.Items) == 0 {
		lines = append(lines, "")
		lines = append(lines, pal.Muted.Render(TruncateToWidth("  probe the endpoint first", width)))
		return lines
	}
	rows := contentH - 4
	if rows <= 0 || rows > len(discovery.Items) {
		rows = len(discovery.Items)
	}
	window, start := window(discovery.Items, maxInt(discovery.Cursor-rows+1, 0), rows)
	for i, item := range window {
		marker := "  "
		box := pal.Muted.Render("○ ")
		if item.Selected {
			box = pal.Selected.Render("● ")
		}
		if start+i == discovery.Cursor {
			marker = pal.Selected.Render("> ")
		}
		tail := ""
		if item.Configured {
			tail = pal.Muted.Render("  configured")
		}
		line := marker + box + sanitizeInline(item.ID) + tail
		lines = append(lines, TruncateToWidth(line, width))
	}
	selected := 0
	for _, item := range discovery.Items {
		if item.Selected {
			selected++
		}
	}
	lines = append(lines, pal.Muted.Render(TruncateToWidth(strings.Repeat("─", maxInt(width, 1)), width)))
	lines = append(lines, TruncateToWidth("  "+pal.Muted.Render(fmt.Sprintf("%d selected · space toggles · a stages", selected)), width))
	return lines
}

// renderCustomDeletePane asks before removing durable configuration.
func renderCustomDeletePane(plane *ModelPlaneState, pal *Palette, width int) []string {
	state := plane.CustomDelete
	if state == nil {
		return nil
	}
	lines := []string{pal.Title.Render(TruncateToWidth("DELETE ENDPOINT", width))}
	lines = append(lines, "")
	lines = append(lines, TruncateToWidth("  Remove "+pal.Bold.Render(sanitizeInline(state.Label))+" from ohgo?", width))
	lines = append(lines, pal.Muted.Render(TruncateToWidth("  Its models disappear from the catalog and role selection.", width)))
	lines = append(lines, "")
	lines = append(lines, TruncateToWidth("  "+pal.Bold.Render("y")+pal.Muted.Render(" delete    ")+pal.Bold.Render("n")+pal.Muted.Render(" cancel"), width))
	return lines
}
