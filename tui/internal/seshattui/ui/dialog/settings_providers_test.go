package dialog

import (
	"strings"
	"testing"

	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/config"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/list"
)

func providerItemNames(items []list.Item) (names []string, more int) {
	for _, it := range items {
		switch v := it.(type) {
		case *settingsProviderItem:
			names = append(names, v.provider.Name)
		case *settingsMoreItem:
			more = v.count
		}
	}
	return names, more
}

func TestProviderListShowsTheMainProvidersAndOneMoreEntry(t *testing.T) {
	s := newSettingsForLayoutTest(t)
	s.gotoView(settingsViewProvidersLLM)

	names, more := providerItemNames(s.provList.FilteredItems())
	if len(names) == 0 || len(names) > 20 {
		t.Fatalf("the main list has %d providers, want the dozen built in: %v", len(names), names)
	}
	if more < 100 {
		t.Fatalf("the more entry counts %d providers, want the catalog's (about 190)", more)
	}
	mainIDs := config.MainProviderIDs()
	for _, item := range s.provList.FilteredItems() {
		if pi, ok := item.(*settingsProviderItem); ok && !mainIDs[string(pi.provider.ID)] {
			t.Errorf("%s is not a main provider and must not be in the first list", pi.provider.Name)
		}
	}
}

func TestMoreEntryOpensTheCatalogProvidersSortedWithoutRegardToCase(t *testing.T) {
	s := newSettingsForLayoutTest(t)
	s.gotoView(settingsViewProvidersLLM)
	s.provMore = true
	s.rebuildProvList("")

	names, more := providerItemNames(s.provList.FilteredItems())
	if more != 0 {
		t.Fatal("the more entry must not be listed inside the more list")
	}
	if len(names) < 100 {
		t.Fatalf("the more list has %d providers", len(names))
	}
	mainIDs := config.MainProviderIDs()
	for _, item := range s.provList.FilteredItems() {
		if pi, ok := item.(*settingsProviderItem); ok && mainIDs[string(pi.provider.ID)] {
			t.Errorf("main provider %s repeated in the more list", pi.provider.Name)
		}
	}
	for i := 1; i < len(names); i++ {
		if strings.ToLower(names[i-1]) > strings.ToLower(names[i]) {
			t.Fatalf("%q comes before %q: the list is not sorted without regard to case", names[i-1], names[i])
		}
	}
}

func TestSearchFindsAProviderThatIsNotAMainOne(t *testing.T) {
	s := newSettingsForLayoutTest(t)
	s.gotoView(settingsViewProvidersLLM)
	s.rebuildProvList("fireworks")

	found := false
	for _, item := range s.provList.FilteredItems() {
		if pi, ok := item.(*settingsProviderItem); ok && strings.Contains(strings.ToLower(pi.provider.Name), "fireworks") {
			found = true
		}
		if _, ok := item.(*settingsMoreItem); ok {
			t.Error("the more entry must not appear in search results")
		}
	}
	if !found {
		t.Fatal("searching for fireworks must find it without opening the more list first")
	}
}

func TestBackFromMoreReturnsToTheMainProviders(t *testing.T) {
	s := newSettingsForLayoutTest(t)
	s.gotoView(settingsViewProvidersLLM)
	s.provMore = true
	s.gotoParent()
	if s.view != settingsViewProvidersLLM || s.provMore {
		t.Fatalf("view %v, provMore %v: back must leave the more list and stay on the providers", s.view, s.provMore)
	}
	s.gotoParent()
	if s.view == settingsViewProvidersLLM {
		t.Fatal("a second back goes up to the Providers page")
	}
}
