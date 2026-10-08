package storage

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"sotnpresetapi/internal/catalog"
)

type FeaturedModAssets interface {
	Upload(context.Context, string, []byte, string) error
	URL(context.Context, string) (string, error)
	Delete(context.Context, string) error
}

// FeaturedMods keeps binary files outside DynamoDB and catalog responses.
type FeaturedMods struct {
	Dynamo
	Assets FeaturedModAssets
}

func (s FeaturedMods) PublishFeaturedMod(ctx context.Context, mod catalog.FeaturedMod, image catalog.FeaturedModImage, patch []byte) (catalog.FeaturedMod, error) {
	if s.Assets == nil {
		return catalog.FeaturedMod{}, errors.New("featured-mod assets are not configured")
	}
	var ext string
	switch image.ContentType {
	case "image/png":
		ext = ".png"
	case "image/jpeg":
		ext = ".jpg"
	case "image/webp":
		ext = ".webp"
	default:
		return catalog.FeaturedMod{}, errors.New("invalid featured-mod image content type")
	}
	if !catalog.ValidID(mod.ID) {
		return catalog.FeaturedMod{}, errors.New("invalid featured-mod ID")
	}
	mod.Image = "images/" + mod.ID + ext
	mod.Patch = "patches/" + mod.ID + ".ppf"
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		for _, key := range []string{mod.Image, mod.Patch} {
			if err := s.Assets.Delete(cleanupCtx, key); err != nil {
				slog.Warn("remove unpublished featured-mod asset", "key", key, "error", err)
			}
		}
	}
	if err := s.Assets.Upload(ctx, mod.Image, image.Data, image.ContentType); err != nil {
		cleanup()
		return catalog.FeaturedMod{}, err
	}
	if err := s.Assets.Upload(ctx, mod.Patch, patch, "application/octet-stream"); err != nil {
		cleanup()
		return catalog.FeaturedMod{}, err
	}
	imageURL, err := s.Assets.URL(ctx, mod.Image)
	if err != nil {
		cleanup()
		return catalog.FeaturedMod{}, err
	}
	if err := s.Dynamo.SaveFeaturedMod(ctx, mod); err != nil {
		// A failed/ambiguous transaction can have committed. Keep its files so a
		// later catalog refresh never exposes a publication with missing assets.
		return catalog.FeaturedMod{}, err
	}
	mod.Image = imageURL
	return mod, nil
}

func (s FeaturedMods) LatestFeaturedMod(ctx context.Context) (catalog.FeaturedMod, error) {
	mod, err := s.Dynamo.LatestFeaturedMod(ctx)
	if err != nil {
		return catalog.FeaturedMod{}, err
	}
	if s.Assets == nil {
		return catalog.FeaturedMod{}, errors.New("featured-mod assets are not configured")
	}
	mod.Image, err = s.Assets.URL(ctx, mod.Image)
	return mod, err
}

func (s FeaturedMods) FeaturedModDownloadURL(ctx context.Context, mod catalog.FeaturedMod) (string, error) {
	if s.Assets == nil {
		return "", errors.New("featured-mod assets are not configured")
	}
	return s.Assets.URL(ctx, mod.Patch)
}
