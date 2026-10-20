package core

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/avanha/pmaas-spi"
)

// fakeOwner is a plugin: it runs what it is given, as the real one does on its goroutine, and counts.
type fakeOwner struct {
	short string
	mu    sync.Mutex
	ran   int
	fail  error
}

func (o *fakeOwner) ShortName() string { return o.short }
func (o *fakeOwner) RouteFullPath(relative string) string {
	return "/plugins/" + o.short + "/" + relative
}

func (o *fakeOwner) ExecVoidFn(target func()) error {
	if o.fail != nil {
		return o.fail
	}

	o.mu.Lock()
	o.ran++
	o.mu.Unlock()

	target()

	return nil
}

func newViewPMAAS() *PMAAS {
	return &PMAAS{entityInfo: map[string]*entityInfo{}}
}

func TestEntityViews_AnEntityWithoutAViewIsKnownByItsNameAndPlugin(t *testing.T) {
	p, owner := newViewPMAAS(), &fakeOwner{short: "bondbridge"}
	p.setEntityInfo("light1", &entityInfo{name: "Bedroom Light", entityType: reflect.TypeFor[string](), owner: owner})

	got := p.getEntityViews([]string{"light1"})[0]

	if got.View != nil || got.Err != nil || got.Name != "Bedroom Light" || got.Plugin != "bondbridge" || got.PluginPath != "/plugins/bondbridge/" {
		t.Errorf("%+v", got)
	}

	if owner.ran != 0 {
		t.Error("nothing should run on the plugin when there is no view")
	}
}

func TestEntityViews_TheViewIsMadeOnThePluginAndKeepsItsKey(t *testing.T) {
	p, owner := newViewPMAAS(), &fakeOwner{short: "bondbridge"}
	p.setEntityInfo("fan1", &entityInfo{name: "Fan", owner: owner})

	if err := p.registerEntityView(owner, "fan1", func() (spi.EntityView, error) {
		return spi.EntityView{Key: "device/1", Value: "the card"}, nil
	}); err != nil {
		t.Fatal(err)
	}

	got := p.getEntityViews([]string{"fan1", "nothing"})

	if got[0].View == nil || got[0].View.Key != "device/1" || got[0].View.Value != "the card" || owner.ran != 1 {
		t.Errorf("%+v ran %d", got[0], owner.ran)
	}

	if got[1].Err == nil || got[1].EntityId != "nothing" {
		t.Errorf("an unknown id should say so: %+v", got[1])
	}
}

func TestEntityViews_AViewThatFailsOrPanicsIsAnErrorForThatEntityOnly(t *testing.T) {
	p, owner := newViewPMAAS(), &fakeOwner{short: "p"}
	p.setEntityInfo("bad", &entityInfo{name: "Bad", owner: owner})
	p.setEntityInfo("worse", &entityInfo{name: "Worse", owner: owner})
	p.setEntityInfo("good", &entityInfo{name: "Good", owner: owner})

	_ = p.registerEntityView(owner, "bad", func() (spi.EntityView, error) { return spi.EntityView{}, errors.New("no data") })
	_ = p.registerEntityView(owner, "worse", func() (spi.EntityView, error) { panic("boom") })
	_ = p.registerEntityView(owner, "good", func() (spi.EntityView, error) { return spi.EntityView{Value: 1}, nil })

	got := p.getEntityViews([]string{"bad", "worse", "good"})

	if got[0].Err == nil || got[0].View != nil || got[1].Err == nil || got[1].View != nil || got[2].Err != nil || got[2].View == nil {
		t.Errorf("%+v", got)
	}
}

func TestEntityViews_OnlyTheOwnerCanGiveAViewAndOnlyToARegisteredEntity(t *testing.T) {
	p, owner, other := newViewPMAAS(), &fakeOwner{short: "a"}, &fakeOwner{short: "b"}
	p.setEntityInfo("e", &entityInfo{name: "E", owner: owner})

	view := func() (spi.EntityView, error) { return spi.EntityView{}, nil }

	if err := p.registerEntityView(other, "e", view); err == nil {
		t.Error("another plugin's view was accepted")
	}

	if err := p.registerEntityView(owner, "missing", view); err == nil {
		t.Error("a view for an unregistered entity was accepted")
	}

	if err := p.registerEntityView(owner, "e", nil); err == nil {
		t.Error("a nil view was accepted")
	}
}

func TestEntityViews_ADeregisteredEntityAndItsViewAreGone(t *testing.T) {
	p, owner := newViewPMAAS(), &fakeOwner{short: "a"}
	p.setEntityInfo("e", &entityInfo{name: "E", owner: owner})
	_ = p.registerEntityView(owner, "e", func() (spi.EntityView, error) { return spi.EntityView{}, nil })

	p.removeEntityInfo("e")

	if got := p.getEntityViews([]string{"e"})[0]; got.Err == nil || p.entityName("e") != "e" {
		t.Errorf("%+v", got)
	}
}

func TestEntityViews_APluginThatCannotRunItGivesAnError(t *testing.T) {
	p, owner := newViewPMAAS(), &fakeOwner{short: "a", fail: errors.New("plugin stopped")}
	p.setEntityInfo("e", &entityInfo{name: "E", owner: owner})
	_ = p.registerEntityView(owner, "e", func() (spi.EntityView, error) { return spi.EntityView{}, nil })

	if got := p.getEntityViews([]string{"e"})[0]; got.Err == nil || got.Name != "E" {
		t.Errorf("%+v", got)
	}
}
