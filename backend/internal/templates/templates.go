// Package templates exposes the starter-app catalog that the
// "Templates" tab uses to create applications without the user
// providing a GitHub URL. Each entry is just a public repo with a
// Dockerfile; "use" hands the URL to the existing build/deploy path.
package templates

import "sort"

// Template describes one starter app.
type Template struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Language      string `json:"language"`
	Description   string `json:"description"`
	RepositoryURL string `json:"repository_url"`
	ContainerPort int    `json:"container_port"`
}

var all = []Template{
	{
		ID:            "node",
		Name:          "Node.js",
		Language:      "Node",
		Description:   "Minimal Node.js HTTP server with a Dockerfile. Exposes port 3000.",
		RepositoryURL: "https://github.com/thelaserunicorn/podium-hello-node",
		ContainerPort: 3000,
	},
	{
		ID:            "python",
		Name:          "Python",
		Language:      "Python",
		Description:   "Minimal Python HTTP server with a Dockerfile. Exposes port 8000.",
		RepositoryURL: "https://github.com/thelaserunicorn/podium-hello-python",
		ContainerPort: 8000,
	},
	{
		ID:            "go",
		Name:          "Go",
		Language:      "Go",
		Description:   "Minimal Go net/http server with a multi-stage Dockerfile. Exposes port 8080.",
		RepositoryURL: "https://github.com/thelaserunicorn/podium-hello-go",
		ContainerPort: 8080,
	},
}

// All returns a sorted copy of the catalog. The copy matters so a
// caller appending to the returned slice doesn't mutate the
// package-level state.
func All() []Template {
	out := make([]Template, len(all))
	copy(out, all)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func Find(id string) (Template, bool) {
	for _, t := range all {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}
