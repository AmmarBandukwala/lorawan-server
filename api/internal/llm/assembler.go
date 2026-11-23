package llm

// toolAssembler reassembles streamed tool-call fragments.
//
// The OpenAI shape streams a tool call in pieces: the id, type and function name on
// one chunk, then argument fragments on the following ones, each tagged with the index
// of the call it belongs to. Assembling these wrongly is not a cosmetic bug — a call
// whose arguments are attributed to the wrong name is a call that executes the wrong
// tool on a live network.
type toolAssembler struct {
	// Calls are keyed by a synthetic slot number rather than by the wire index. The
	// two coincide in the normal case; they diverge when a runtime reuses index 0 for
	// several calls, which is the case below that would otherwise merge them.
	bySlot map[int]*ToolCall
	// slotOf maps a wire index to the slot currently receiving its fragments.
	slotOf map[int]int
	// slots records slot numbers in the order their calls first appeared, so the
	// result is stable. Map iteration would shuffle parallel calls between two
	// identical requests, making a transcript unreproducible for no reason.
	slots []int
	next  int
}

func newToolAssembler() *toolAssembler {
	return &toolAssembler{bySlot: map[int]*ToolCall{}, slotOf: map[int]int{}}
}

// add folds one partial into the call it belongs to.
func (a *toolAssembler) add(delta wireToolDelta) {
	slot, known := a.slotOf[delta.Index]
	call := a.bySlot[slot]

	// A delta carrying a different id at an index already occupied means the runtime
	// is reusing index numbers rather than assigning one per call — Ollama has done
	// this. Treating it as a continuation would merge two distinct calls into one with
	// concatenated arguments and a single name: a call that cannot be executed, and an
	// error that points nowhere. Give it a slot of its own instead.
	if known && delta.ID != "" && call.ID != "" && call.ID != delta.ID {
		known = false
	}

	if !known {
		slot = a.next
		a.next++
		call = &ToolCall{
			ID:       delta.ID,
			Type:     delta.Type,
			Function: FunctionCall{Name: delta.Function.Name},
		}
		if call.Type == "" {
			// Not every runtime repeats the type on the creating delta, and the
			// endpoint expects it back verbatim on the replay.
			call.Type = "function"
		}
		call.Function.Arguments = delta.Function.Arguments
		a.bySlot[slot] = call
		a.slotOf[delta.Index] = slot
		a.slots = append(a.slots, slot)
		return
	}

	// Continuation. Fields are only overwritten when present, because an empty string
	// in a delta means "unchanged", not "now empty" — assigning unconditionally would
	// erase the name as soon as the first argument fragment arrived.
	if delta.ID != "" {
		call.ID = delta.ID
	}
	if delta.Type != "" {
		call.Type = delta.Type
	}
	// The name is concatenated rather than assigned: some runtimes split it across
	// chunks along with the arguments, and assigning would keep only the last piece.
	if delta.Function.Name != "" {
		call.Function.Name += delta.Function.Name
	}
	call.Function.Arguments += delta.Function.Arguments
}

// ordered returns the assembled calls in the order they first appeared.
func (a *toolAssembler) ordered() []ToolCall {
	if len(a.slots) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(a.slots))
	for _, slot := range a.slots {
		if call, ok := a.bySlot[slot]; ok {
			out = append(out, *call)
		}
	}
	return out
}
