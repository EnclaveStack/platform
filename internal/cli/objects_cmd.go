package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/enclavestack/platform/pkg/api"
)

// Bucket objects (L2-002 CLI-55, L2-013). `ls`, `get` and `rm` already name the bucket itself, so the
// object commands say "object".

// parseTags reads --tag NAME=VALUE flags.
func parseTags(list []string) (map[string]string, error) {
	tags := map[string]string{}
	for _, s := range list {
		k, v, ok := strings.Cut(s, "=")
		if !ok || k == "" {
			return nil, usagef("--tag needs NAME=VALUE, like video-id=v42; got %q", s)
		}
		tags[k] = v
	}
	return tags, nil
}

func (a *app) bucketObjectCmds() []*cobra.Command {
	var recursive bool
	objects := &cobra.Command{Use: "objects NAME [FOLDER]", Short: "List a bucket's folders and files", Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, r, err := a.resolve(cmd.Context(), api.KindBucket, args[0])
			if err != nil {
				return err
			}
			prefix := ""
			if len(args) == 2 && args[1] != "" {
				prefix = strings.TrimSuffix(strings.TrimPrefix(args[1], "/"), "/") + "/"
			}
			var all api.ObjectList
			all.Prefix, all.Folders, all.Items = prefix, []string{}, []api.ObjectInfo{}
			queue := []string{prefix}
			for len(queue) > 0 {
				p := queue[0]
				queue = queue[1:]
				cursor := ""
				for {
					page, err := c.ListObjects(cmd.Context(), r.ID, p, cursor)
					if err != nil {
						return err
					}
					if recursive {
						queue = append(queue, page.Folders...)
					} else {
						all.Folders = append(all.Folders, page.Folders...)
					}
					all.Items = append(all.Items, page.Items...)
					if page.NextCursor == nil {
						break
					}
					cursor = *page.NextCursor
				}
			}
			if a.output == "json" {
				return a.json(all)
			}
			if len(all.Folders)+len(all.Items) == 0 {
				fmt.Fprintf(a.io.Out, "No files in %s/%s. Add one with: enclave bucket upload %s FILE\n", r.Name, prefix, r.Name)
				return nil
			}
			rows := [][]string{}
			for _, f := range all.Folders {
				rows = append(rows, []string{f, "folder", "", ""})
			}
			for _, o := range all.Items {
				rows = append(rows, []string{o.Key, api.ByteSize(o.Size), o.Modified.Local().Format(time.DateTime), o.ContentType})
			}
			a.table([]string{"KEY", "SIZE", "CHANGED", "TYPE"}, rows)
			return nil
		}}
	objects.Flags().BoolVarP(&recursive, "recursive", "r", false, "list every file below the folder")

	var tagList []string
	upload := &cobra.Command{Use: "upload NAME FILE [KEY]", Short: "Upload a file (KEY defaults to the file's name; a KEY ending in / is a folder)", Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			tags, err := parseTags(tagList)
			if err != nil {
				return err
			}
			f, err := os.Open(args[1])
			if err != nil {
				return err
			}
			defer f.Close()
			st, err := f.Stat()
			if err != nil {
				return err
			}
			key := filepath.Base(args[1])
			if len(args) == 3 {
				key = args[2]
				if strings.HasSuffix(key, "/") {
					key += filepath.Base(args[1])
				}
			}
			c, r, err := a.resolve(cmd.Context(), api.KindBucket, args[0])
			if err != nil {
				return err
			}
			info, err := c.PutObject(cmd.Context(), r.ID, key, f, st.Size(), tags)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.json(info)
			}
			fmt.Fprintf(a.io.Out, "Uploaded %s to %s/%s.\n", api.ByteSize(info.Size), r.Name, info.Key)
			return nil
		}}
	upload.Flags().StringArrayVar(&tagList, "tag", nil, "a tag on the file, NAME=VALUE (repeatable), like video-id=v42; transcoders carry tags to their outputs")

	download := &cobra.Command{Use: "download NAME KEY [FILE]", Short: "Download a file (FILE defaults to the key's name; - writes to standard output)", Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, r, err := a.resolve(cmd.Context(), api.KindBucket, args[0])
			if err != nil {
				return err
			}
			_, body, err := c.GetObject(cmd.Context(), r.ID, args[1])
			if err != nil {
				return err
			}
			defer body.Close()
			dest := filepath.Base(args[1])
			if len(args) == 3 {
				dest = args[2]
			}
			return a.writeDownload(dest, body)
		}}

	var yes bool
	del := &cobra.Command{Use: "delete-object NAME KEY", Short: "Delete a file from a bucket", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, r, err := a.resolve(cmd.Context(), api.KindBucket, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("Delete %s from the bucket %s?", args[1], r.Name), yes); err != nil {
				return err
			}
			if err := c.DeleteObject(cmd.Context(), r.ID, args[1]); err != nil {
				return err
			}
			fmt.Fprintf(a.io.Out, "Deleted %s/%s.\n", r.Name, args[1])
			return nil
		}}
	del.Flags().BoolVar(&yes, "yes", false, "don't ask")
	return []*cobra.Command{objects, upload, download, del}
}

// writeDownload writes a file, or standard output for "-". A partial file is removed.
func (a *app) writeDownload(dest string, body io.Reader) error {
	if dest == "-" {
		_, err := io.Copy(a.io.Out, body)
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dest)
		return err
	}
	fmt.Fprintf(a.io.Err, "Downloaded %s to %s.\n", api.ByteSize(n), dest)
	return nil
}
