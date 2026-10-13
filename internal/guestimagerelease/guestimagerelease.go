package guestimagerelease

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/guestimage"
)

type Options struct {
	Base    string
	Target  string
	Version string
	Commit  string
	Client  *http.Client
}

func Source(ctx context.Context, o Options) (string, error) {
	if o.Base == "" {
		return "", errors.New("guestimagerelease: no base to download a guest image from")
	}
	if o.Target == "" {
		return "", errors.New("guestimagerelease: this host has no guest image target")
	}
	base := strings.TrimSuffix(o.Base, "/")
	refs := make([]string, 0, 2)
	if o.Version != "" {
		refs = append(refs, "releases/"+o.Version)
	}
	if o.Commit != "" {
		refs = append(refs, "main/"+o.Commit)
	}
	if len(refs) == 0 {
		return "", errors.New("guestimagerelease: this build carries neither a version nor a commit, so no guest image can be named for it")
	}
	client := o.Client
	if client == nil {
		client = http.DefaultClient
	}
	tried := make([]string, 0, len(refs))
	for _, ref := range refs {
		src := base + "/" + ref + "/guest-image/" + o.Target
		ok, err := published(ctx, client, src+"/"+guestimage.ManifestName)
		if err != nil {
			return "", err
		}
		if ok {
			return src, nil
		}
		tried = append(tried, src)
	}
	return "", fmt.Errorf("guestimagerelease: no published guest image for target %s, tried %s", o.Target, strings.Join(tried, ", "))
}

func published(ctx context.Context, client *http.Client, url string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("guestimagerelease: %s: %w", url, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound, http.StatusForbidden:
		return false, nil
	}
	return false, fmt.Errorf("guestimagerelease: %s answered %s", url, resp.Status)
}
