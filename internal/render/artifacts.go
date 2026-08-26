package render

// Artifact describes what to render and how the result should be served.
// Kept together so a new artifact cannot be added with a content type but no
// cache policy.
type Artifact struct {
	// Path is appended to the site URL.
	Path string
	// Key is the object key, and also the name the site links to.
	Key          string
	ContentType  string
	CacheControl string
}

// CVPDF is the downloadable CV.
//
// A short max-age, because a CV can be updated minutes before someone is sent
// the link. stale-while-revalidate lets the edge answer instantly and refresh
// behind the request, so freshness costs nobody a wait.
var CVPDF = Artifact{
	Path:         "/cv",
	Key:          "billy-watson-cv.pdf",
	ContentType:  "application/pdf",
	CacheControl: "public, max-age=600, stale-while-revalidate=3600",
}
