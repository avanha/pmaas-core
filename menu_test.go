package core

import (
	"testing"

	spi "github.com/avanha/pmaas-spi"
)

func newMenuPMAAS() *PMAAS {
	return &PMAAS{menuEntriesByShortName: map[string]*spi.MenuEntry{}}
}

func labels(menu []spi.MenuEntry) []string {
	var result []string

	for _, entry := range menu {
		if entry.Separator {
			result = append(result, "---")
		} else {
			result = append(result, entry.Label)
		}
	}

	return result
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func registerList(p *PMAAS, short string, label string, priority int, override *int) {
	p.registerMenuRoute(short, "/plugins/"+short+"/", "", &spi.HttpHandlerOptions{MenuLabel: label, MenuPriority: priority}, override)
}

func TestGetMenu_PinnedEntriesFirstThenARuleThenTheRestAlphabetically(t *testing.T) {
	p := newMenuPMAAS()

	// Registered in no particular order, as plugins are.
	registerList(p, "netmon", "Network", 0, nil)
	registerList(p, "automations", "Automations", 80, nil)
	registerList(p, "bluetooth", "Bluetooth", 0, nil)
	registerList(p, "alerts", "Alerts", 90, nil)
	registerList(p, "esphome", "esphome", 0, nil) // lower case sorts with the others
	_ = p.provideRootStatusHandler(nil, nil)

	got := labels(p.getMenu())
	want := []string{"Status", "Alerts", "Automations", "---", "Bluetooth", "esphome", "Network"}

	if !equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestGetMenu_TheAssemblyCanOverrideAPluginsPriority(t *testing.T) {
	p := newMenuPMAAS()
	raise, sink := 95, -1

	registerList(p, "automations", "Automations", 80, &raise) // above Alerts
	registerList(p, "alerts", "Alerts", 90, nil)
	registerList(p, "netmon", "Network", 0, nil)
	registerList(p, "porkbun", "Porkbun", 0, &sink) // below the rest, with no rule of its own
	registerList(p, "acme", "ACME", 0, nil)
	_ = p.provideRootStatusHandler(nil, nil)

	got := labels(p.getMenu())
	want := []string{"Status", "Automations", "Alerts", "---", "ACME", "Network", "Porkbun"}

	if !equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestGetMenu_NoRuleWhenThereIsNothingOnOneSideOfIt(t *testing.T) {
	onlyOthers := newMenuPMAAS()
	registerList(onlyOthers, "b", "B", 0, nil)
	registerList(onlyOthers, "a", "A", 0, nil)

	if got := labels(onlyOthers.getMenu()); !equal(got, []string{"A", "B"}) {
		t.Errorf("%v", got)
	}

	onlyPinned := newMenuPMAAS()
	registerList(onlyPinned, "b", "B", 5, nil)
	_ = onlyPinned.provideRootStatusHandler(nil, nil)

	if got := labels(onlyPinned.getMenu()); !equal(got, []string{"Status", "B"}) {
		t.Errorf("%v", got)
	}
}

func TestGetMenu_TheStatusPriorityCanBeOverriddenToo(t *testing.T) {
	p := newMenuPMAAS()
	low := 0

	registerList(p, "alerts", "Alerts", 90, nil)
	registerList(p, "zeta", "Zeta", 0, nil)
	_ = p.provideRootStatusHandler(nil, &low)

	if got := labels(p.getMenu()); !equal(got, []string{"Alerts", "---", "Status", "Zeta"}) {
		t.Errorf("%v", got)
	}
}

func TestGetMenu_ReturnsACopy(t *testing.T) {
	p := newMenuPMAAS()
	registerList(p, "a", "A", 0, nil)

	p.getMenu()[0].Label = "changed"

	if got := labels(p.getMenu()); !equal(got, []string{"A"}) {
		t.Errorf("%v", got)
	}
}
