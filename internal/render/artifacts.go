package render

// Artifact describes what to render and how the result should be served.
// Kept together so a new artifact cannot be added with a content type but no
// cache policy.
type Artifact struct {
	// Paths are appended to the site URL. More than one where an artifact is
	// derived from several pages — the content hash combines them, so a change
	// to any of them re-makes the whole set.
	Paths []string
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
	Paths:        []string{"/cv"},
	Key:          "billy-watson-cv.pdf",
	ContentType:  "application/pdf",
	CacheControl: "public, max-age=600, stale-while-revalidate=3600",
}

// StartupImagesArtifact is the PWA splash-screen set: two pages at eleven
// device sizes, twenty-two objects.
//
// Key is where the content hash is recorded, not an object key — there are
// twenty-two of those, and StartupKey builds them.
//
// A day's cache, because nobody notices a splash screen that lags the site by
// an afternoon, and these are fetched in bursts when a device installs the
// PWA — exactly when a cache earns its keep.
var StartupImagesArtifact = Artifact{
	Paths:        []string{"/", "/cv"},
	Key:          "startup-images",
	ContentType:  "image/png",
	CacheControl: "public, max-age=86400, stale-while-revalidate=604800",
}
