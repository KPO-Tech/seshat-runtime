package catalog

// Merge puts a catalog fetched at run time over the trusted one (the embedded snapshot) and returns the result. The rule is the
// security rule of the package:
//
//   - A provider that is in trusted keeps its BaseURL, Env and Protocol from trusted. The fetched catalog may only update its
//     name and its models. A fetched catalog that moves a provider to another URL changes nothing here.
//   - A provider that is only in fetched is added with Unverified set: a host must show its URL and have the user confirm it
//     before an API key is sent there.
//   - A model that is only in trusted is kept (a provider that drops a model from its list does not make a conversation that
//     uses it fail); a model in both takes the fetched data.
func Merge(trusted, fetched *Catalog) *Catalog {
	if trusted == nil {
		return markUnverified(fetched)
	}
	if fetched == nil {
		return trusted
	}
	out := &Catalog{Providers: make(map[string]Provider, len(trusted.Providers)+len(fetched.Providers)), Source: fetched.Source}
	for id, tp := range trusted.Providers {
		merged := tp
		merged.Models = make(map[string]Model, len(tp.Models))
		for mid, m := range tp.Models {
			merged.Models[mid] = m
		}
		if fp, ok := fetched.Providers[id]; ok {
			if fp.Name != "" {
				merged.Name = fp.Name
			}
			for mid, m := range fp.Models {
				merged.Models[mid] = m
			}
		}
		out.Providers[id] = merged
	}
	for id, fp := range fetched.Providers {
		if _, known := trusted.Providers[id]; known {
			continue
		}
		fp.Unverified = true
		out.Providers[id] = fp
	}
	return out
}

func markUnverified(c *Catalog) *Catalog {
	if c == nil {
		return nil
	}
	out := &Catalog{Providers: make(map[string]Provider, len(c.Providers)), Source: c.Source}
	for id, p := range c.Providers {
		p.Unverified = true
		out.Providers[id] = p
	}
	return out
}
