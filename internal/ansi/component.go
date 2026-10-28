package ansi

import "time"

// Size is the terminal area available to a component.
type Size struct {
	Width  int
	Height int
}

// Position is a zero-based terminal cell coordinate.
type Position struct {
	Row int
	Col int
}

// Frame is a complete component view. Rows must not contain line separators.
type Frame struct {
	Rows   []string
	Cursor Position
}

// Result describes the effect of one component update.
type Result struct {
	Redraw      bool
	ClearScreen bool
	Submitted   bool
	Submit      string
	Exit        bool
	Err         error
	External    bool
}

// Component is the minimal contract between terminal runtime and UI state.
type Component interface {
	Update(Event) Result
	View(Size) Frame
}

// Runtime dispatches terminal events to one component and renders its frames.
type Runtime struct {
	terminal *Terminal
	screen   *Screen
}

func NewRuntime(terminal *Terminal) *Runtime {
	return &Runtime{terminal: terminal, screen: NewScreen(terminal.out)}
}

func (r *Runtime) Run(component Component) Result {
	return r.RunWithWake(component, nil)
}

// RunWithWake additionally suspends and clears the component when wake fires.
// Component state is retained, allowing the caller to redraw it afterward.
func (r *Runtime) RunWithWake(component Component, wake <-chan struct{}) Result {
	restore, err := r.terminal.BeginInput()
	if err != nil {
		return Result{Err: err}
	}
	defer restore()

	r.screen.Draw(component.View(r.terminal.Size()))
	for {
		if wake != nil {
			select {
			case <-wake:
				r.screen.Clear()
				return Result{External: true}
			default:
			}
		}
		event, ready, err := r.terminal.ReadEventWithin(50 * time.Millisecond)
		if err != nil {
			r.screen.Clear()
			return Result{Err: err}
		}
		if !ready {
			continue
		}
		result := component.Update(event)
		if result.Err != nil || result.Exit || result.Submitted {
			r.screen.Clear()
			return result
		}
		if result.ClearScreen {
			r.screen.ClearAll()
			r.screen.Draw(component.View(r.terminal.Size()))
		} else if result.Redraw {
			r.screen.Draw(component.View(r.terminal.Size()))
		}
	}
}

// ClearScreen clears the full terminal and resets the runtime's frame state.
func (r *Runtime) ClearScreen() {
	r.screen.ClearAll()
}
