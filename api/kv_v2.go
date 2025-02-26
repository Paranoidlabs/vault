package api

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/mitchellh/mapstructure"
)

type kvv2 struct {
	c         *Client
	mountPath string
}

// KVMetadata is the full metadata for a given KV v2 secret.
type KVMetadata struct {
	CASRequired        bool                   `mapstructure:"cas_required"`
	CreatedTime        time.Time              `mapstructure:"created_time"`
	CurrentVersion     int                    `mapstructure:"current_version"`
	CustomMetadata     map[string]interface{} `mapstructure:"custom_metadata"`
	DeleteVersionAfter time.Duration          `mapstructure:"delete_version_after"`
	MaxVersions        int                    `mapstructure:"max_versions"`
	OldestVersion      int                    `mapstructure:"oldest_version"`
	UpdatedTime        time.Time              `mapstructure:"updated_time"`
	// Keys are stringified ints, e.g. "3". To get a sorted slice of version metadata, use GetVersionsAsList.
	Versions map[string]KVVersionMetadata `mapstructure:"versions"`
	Raw      *Secret
}

// KVVersionMetadata is a subset of metadata for a given version of a KV v2 secret.
type KVVersionMetadata struct {
	Version      int       `mapstructure:"version"`
	CreatedTime  time.Time `mapstructure:"created_time"`
	DeletionTime time.Time `mapstructure:"deletion_time"`
	Destroyed    bool      `mapstructure:"destroyed"`
}

// Currently supported options: WithOption, WithCheckAndSet, WithMethod
type KVOption func() (key string, value interface{})

const (
	KVOptionCheckAndSet    = "cas"
	KVOptionMethod         = "method"
	KVMergeMethodPatch     = "patch"
	KVMergeMethodReadWrite = "rw"
)

// WithOption can optionally be passed to provide generic options for a
// KV request. Valid keys and values depend on the type of request.
func WithOption(key string, value interface{}) KVOption {
	return func() (string, interface{}) {
		return key, value
	}
}

// WithCheckAndSet can optionally be passed to perform a check-and-set
// operation on a KV request. If not set, the write will be allowed.
// If cas is set to 0, a write will only be allowed if the key doesn't exist.
// If set to non-zero, the write will only be allowed if the key’s current
// version matches the version specified in the cas parameter.
func WithCheckAndSet(cas int) KVOption {
	return WithOption(KVOptionCheckAndSet, cas)
}

// WithMergeMethod can optionally be passed to dictate which type of
// patch to perform in a Patch request. If set to "patch", then an HTTP PATCH
// request will be issued. If set to "rw", then a read will be performed,
// then a local update, followed by a remote update. Defaults to "patch".
func WithMergeMethod(method string) KVOption {
	return WithOption(KVOptionMethod, method)
}

// Get returns the latest version of a secret from the KV v2 secrets engine.
//
// If the latest version has been deleted, an error will not be thrown, but
// the Data field on the returned secret will be nil, and the Metadata field
// will contain the deletion time.
func (kv *kvv2) Get(ctx context.Context, secretPath string) (*KVSecret, error) {
	pathToRead := fmt.Sprintf("%s/data/%s", kv.mountPath, secretPath)

	secret, err := kv.c.Logical().ReadWithContext(ctx, pathToRead)
	if err != nil {
		return nil, fmt.Errorf("error encountered while reading secret at %s: %w", pathToRead, err)
	}
	if secret == nil {
		return nil, fmt.Errorf("no secret found at %s", pathToRead)
	}

	kvSecret, err := extractDataAndVersionMetadata(secret)
	if err != nil {
		return nil, fmt.Errorf("error parsing secret at %s: %w", pathToRead, err)
	}

	cm, err := extractCustomMetadata(secret)
	if err != nil {
		return nil, fmt.Errorf("error reading custom metadata for secret at %s: %w", pathToRead, err)
	}
	kvSecret.CustomMetadata = cm

	return kvSecret, nil
}

// GetVersion returns the data and metadata for a specific version of the
// given secret.
//
// If that version has been deleted, the Data field on the
// returned secret will be nil, and the Metadata field will contain the deletion time.
//
// GetVersionsAsList can provide a list of available versions sorted by
// version number, while the response from GetMetadata contains them as a map.
func (kv *kvv2) GetVersion(ctx context.Context, secretPath string, version int) (*KVSecret, error) {
	pathToRead := fmt.Sprintf("%s/data/%s", kv.mountPath, secretPath)

	queryParams := map[string][]string{"version": {strconv.Itoa(version)}}
	secret, err := kv.c.Logical().ReadWithDataWithContext(ctx, pathToRead, queryParams)
	if err != nil {
		return nil, err
	}
	if secret == nil {
		return nil, fmt.Errorf("no secret with version %d found at %s", version, pathToRead)
	}

	kvSecret, err := extractDataAndVersionMetadata(secret)
	if err != nil {
		return nil, fmt.Errorf("error parsing secret at %s: %w", pathToRead, err)
	}

	cm, err := extractCustomMetadata(secret)
	if err != nil {
		return nil, fmt.Errorf("error reading custom metadata for secret at %s: %w", pathToRead, err)
	}
	kvSecret.CustomMetadata = cm

	return kvSecret, nil
}

// GetVersionsAsList returns a subset of the metadata for each version of the secret, sorted by version number.
func (kv *kvv2) GetVersionsAsList(ctx context.Context, secretPath string) ([]KVVersionMetadata, error) {
	pathToRead := fmt.Sprintf("%s/metadata/%s", kv.mountPath, secretPath)

	secret, err := kv.c.Logical().ReadWithContext(ctx, pathToRead)
	if err != nil {
		return nil, err
	}
	if secret == nil || secret.Data == nil {
		return nil, fmt.Errorf("no secret metadata found at %s", pathToRead)
	}

	md, err := extractFullMetadata(secret)
	if err != nil {
		return nil, fmt.Errorf("unable to extract metadata from secret to determine versions: %w", err)
	}

	versionsList := make([]KVVersionMetadata, 0, len(md.Versions))
	for _, versionMetadata := range md.Versions {
		versionsList = append(versionsList, versionMetadata)
	}

	sort.Slice(versionsList, func(i, j int) bool { return versionsList[i].Version < versionsList[j].Version })
	return versionsList, nil
}

// GetMetadata returns the full metadata for a given secret, including a map of
// its existing versions and their respective creation/deletion times, etc.
func (kv *kvv2) GetMetadata(ctx context.Context, secretPath string) (*KVMetadata, error) {
	pathToRead := fmt.Sprintf("%s/metadata/%s", kv.mountPath, secretPath)

	secret, err := kv.c.Logical().ReadWithContext(ctx, pathToRead)
	if err != nil {
		return nil, err
	}
	if secret == nil || secret.Data == nil {
		return nil, fmt.Errorf("no secret metadata found at %s", pathToRead)
	}

	md, err := extractFullMetadata(secret)
	if err != nil {
		return nil, fmt.Errorf("unable to extract metadata from secret: %w", err)
	}

	return md, nil
}

// Put inserts a key-value secret (e.g. {"password": "Hashi123"})
// into the KV v2 secrets engine.
//
// If the secret already exists, a new version will be created
// and the previous version can be accessed with the GetVersion method.
// GetMetadata can provide a list of available versions.
func (kv *kvv2) Put(ctx context.Context, secretPath string, data map[string]interface{}, opts ...KVOption) (*KVSecret, error) {
	pathToWriteTo := fmt.Sprintf("%s/data/%s", kv.mountPath, secretPath)

	wrappedData := map[string]interface{}{
		"data": data,
	}

	// Add options such as check-and-set, etc.
	// We leave this as an optional arg so that most users
	// can just pass plain key-value secret data without
	// having to remember to put the extra layer "data" in there.
	options := make(map[string]interface{})
	for _, opt := range opts {
		k, v := opt()
		options[k] = v
	}
	if len(opts) > 0 {
		wrappedData["options"] = options
	}

	secret, err := kv.c.Logical().WriteWithContext(ctx, pathToWriteTo, wrappedData)
	if err != nil {
		return nil, fmt.Errorf("error writing secret to %s: %w", pathToWriteTo, err)
	}
	if secret == nil {
		return nil, fmt.Errorf("no secret was written to %s", pathToWriteTo)
	}

	metadata, err := extractVersionMetadata(secret)
	if err != nil {
		return nil, fmt.Errorf("secret was written successfully, but unable to view version metadata from response: %w", err)
	}

	kvSecret := &KVSecret{
		Data:            nil, // secret.Data in this case is the metadata
		VersionMetadata: metadata,
		Raw:             secret,
	}

	cm, err := extractCustomMetadata(secret)
	if err != nil {
		return nil, fmt.Errorf("error reading custom metadata for secret at %s: %w", pathToWriteTo, err)
	}
	kvSecret.CustomMetadata = cm

	return kvSecret, nil
}

// Patch additively updates the most recent version of a key-value secret,
// differentiating it from Put which will fully overwrite the previous data.
// Only the key-value pairs that are new or changing need to be provided.
//
// The WithMethod KVOption function can optionally be passed to dictate which
// kind of patch to perform, as older Vault server versions (pre-1.9.0) may
// only be able to use the old "rw" (read-then-write) style of partial update,
// whereas newer Vault servers can use the default value of "patch" if the
// client token's policy has the "patch" capability.
func (kv *kvv2) Patch(ctx context.Context, secretPath string, newData map[string]interface{}, opts ...KVOption) (*KVSecret, error) {
	// determine patch method
	var patchMethod string
	var ok bool
	for _, opt := range opts {
		k, v := opt()
		if k == "method" {
			patchMethod, ok = v.(string)
			if !ok {
				return nil, fmt.Errorf("unsupported type provided for option value; value for patch method should be string \"rw\" or \"patch\"")
			}
		}
	}

	// Determine which kind of patch to use,
	// the newer HTTP Patch style or the older read-then-write style
	var kvs *KVSecret
	var perr error
	switch patchMethod {
	case "rw":
		kvs, perr = readThenWrite(ctx, kv.c, kv.mountPath, secretPath, newData)
	case "patch":
		kvs, perr = mergePatch(ctx, kv.c, kv.mountPath, secretPath, newData, opts...)
	case "":
		kvs, perr = mergePatch(ctx, kv.c, kv.mountPath, secretPath, newData, opts...)
	default:
		return nil, fmt.Errorf("unsupported patch method provided; value for patch method should be string \"rw\" or \"patch\"")
	}
	if perr != nil {
		return nil, fmt.Errorf("unable to perform patch: %w", perr)
	}
	if kvs == nil {
		return nil, fmt.Errorf("no secret was written to %s", secretPath)
	}

	return kvs, nil
}

// Delete deletes the most recent version of a secret from the KV v2
// secrets engine. To delete an older version, use DeleteVersions.
func (kv *kvv2) Delete(ctx context.Context, secretPath string) error {
	pathToDelete := fmt.Sprintf("%s/data/%s", kv.mountPath, secretPath)

	_, err := kv.c.Logical().DeleteWithContext(ctx, pathToDelete)
	if err != nil {
		return fmt.Errorf("error deleting secret at %s: %w", pathToDelete, err)
	}

	return nil
}

// DeleteVersions deletes the specified versions of a secret from the KV v2
// secrets engine. To delete the latest version of a secret, just use Delete.
func (kv *kvv2) DeleteVersions(ctx context.Context, secretPath string, versions []int) error {
	// verb and path are different when trying to delete past versions
	pathToDelete := fmt.Sprintf("%s/delete/%s", kv.mountPath, secretPath)

	if len(versions) == 0 {
		return nil
	}

	var versionsToDelete []string
	for _, version := range versions {
		versionsToDelete = append(versionsToDelete, strconv.Itoa(version))
	}
	versionsMap := map[string]interface{}{
		"versions": versionsToDelete,
	}
	_, err := kv.c.Logical().WriteWithContext(ctx, pathToDelete, versionsMap)
	if err != nil {
		return fmt.Errorf("error deleting secret at %s: %w", pathToDelete, err)
	}

	return nil
}

// DeleteMetadata deletes all versions and metadata of the secret at the
// given path.
func (kv *kvv2) DeleteMetadata(ctx context.Context, secretPath string) error {
	pathToDelete := fmt.Sprintf("%s/metadata/%s", kv.mountPath, secretPath)

	_, err := kv.c.Logical().DeleteWithContext(ctx, pathToDelete)
	if err != nil {
		return fmt.Errorf("error deleting secret metadata at %s: %w", pathToDelete, err)
	}

	return nil
}

// Undelete undeletes the given versions of a secret, restoring the data
// so that it can be fetched again with Get requests.
//
// A list of existing versions can be retrieved using the GetVersionsAsList method.
func (kv *kvv2) Undelete(ctx context.Context, secretPath string, versions []int) error {
	pathToUndelete := fmt.Sprintf("%s/undelete/%s", kv.mountPath, secretPath)

	data := map[string]interface{}{
		"versions": versions,
	}

	_, err := kv.c.Logical().WriteWithContext(ctx, pathToUndelete, data)
	if err != nil {
		return fmt.Errorf("error undeleting secret metadata at %s: %w", pathToUndelete, err)
	}

	return nil
}

// Destroy permanently removes the specified secret versions' data
// from the Vault server. If no secret exists at the given path, no
// action will be taken.
//
// A list of existing versions can be retrieved using the GetVersionsAsList method.
func (kv *kvv2) Destroy(ctx context.Context, secretPath string, versions []int) error {
	pathToDestroy := fmt.Sprintf("%s/destroy/%s", kv.mountPath, secretPath)

	data := map[string]interface{}{
		"versions": versions,
	}

	_, err := kv.c.Logical().WriteWithContext(ctx, pathToDestroy, data)
	if err != nil {
		return fmt.Errorf("error destroying secret metadata at %s: %w", pathToDestroy, err)
	}

	return nil
}

// Rollback can be used to roll a secret back to a previous
// non-deleted/non-destroyed version. That previous version becomes the
// next/newest version for the path.
func (kv *kvv2) Rollback(ctx context.Context, secretPath string, toVersion int) (*KVSecret, error) {
	// First, do a read to get the current version for check-and-set
	latest, err := kv.Get(ctx, secretPath)
	if err != nil {
		return nil, fmt.Errorf("unable to get latest version of secret: %w", err)
	}

	// Make sure a value already exists
	if latest == nil {
		return nil, fmt.Errorf("no secret was found: %w", err)
	}

	// Verify metadata found
	if latest.VersionMetadata == nil {
		return nil, fmt.Errorf("no metadata found; rollback can only be used on existing data")
	}

	// Now run it again and read the version we want to roll back to
	rollbackVersion, err := kv.GetVersion(ctx, secretPath, toVersion)
	if err != nil {
		return nil, fmt.Errorf("unable to get previous version %d of secret: %s", toVersion, err)
	}

	err = validateRollbackVersion(rollbackVersion)
	if err != nil {
		return nil, fmt.Errorf("invalid rollback version %d: %w", toVersion, err)
	}

	casVersion := latest.VersionMetadata.Version
	kvs, err := kv.Put(ctx, secretPath, rollbackVersion.Data, WithCheckAndSet(casVersion))
	if err != nil {
		return nil, fmt.Errorf("unable to roll back to previous secret version: %w", err)
	}

	return kvs, nil
}

func extractCustomMetadata(secret *Secret) (map[string]interface{}, error) {
	// Logical Writes return the metadata directly, Reads return it nested inside the "metadata" key
	cmI, ok := secret.Data["custom_metadata"]
	if !ok {
		mI, ok := secret.Data["metadata"]
		if !ok { // if that's not found, bail since it should have had one or the other
			return nil, fmt.Errorf("secret is missing expected fields")
		}
		mM, ok := mI.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("unexpected type for 'metadata' element: %T (%#v)", mI, mI)
		}
		cmI, ok = mM["custom_metadata"]
		if !ok {
			return nil, fmt.Errorf("metadata missing expected field \"custom_metadata\":%v", mM)
		}
	}

	cm, ok := cmI.(map[string]interface{})
	if !ok && cmI != nil {
		return nil, fmt.Errorf("unexpected type for 'metadata' element: %T (%#v)", cmI, cmI)
	}

	return cm, nil
}

func extractDataAndVersionMetadata(secret *Secret) (*KVSecret, error) {
	// A nil map is a valid value for data: secret.Data will be nil when this
	// version of the secret has been deleted, but the metadata is still
	// available.
	var data map[string]interface{}
	if secret.Data != nil {
		dataInterface, ok := secret.Data["data"]
		if !ok {
			return nil, fmt.Errorf("missing expected 'data' element")
		}

		if dataInterface != nil {
			data, ok = dataInterface.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("unexpected type for 'data' element: %T (%#v)", data, data)
			}
		}
	}

	metadata, err := extractVersionMetadata(secret)
	if err != nil {
		return nil, fmt.Errorf("unable to get version metadata: %w", err)
	}

	return &KVSecret{
		Data:            data,
		VersionMetadata: metadata,
		Raw:             secret,
	}, nil
}

func extractVersionMetadata(secret *Secret) (*KVVersionMetadata, error) {
	var metadata *KVVersionMetadata

	if secret.Data == nil {
		return nil, nil
	}

	// Logical Writes return the metadata directly, Reads return it nested inside the "metadata" key
	var metadataMap map[string]interface{}
	metadataInterface, ok := secret.Data["metadata"]
	if ok {
		metadataMap, ok = metadataInterface.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("unexpected type for 'metadata' element: %T (%#v)", metadataInterface, metadataInterface)
		}
	} else {
		metadataMap = secret.Data
	}

	// deletion_time usually comes in as an empty string which can't be
	// processed as time.RFC3339, so we reset it to a convertible value
	if metadataMap["deletion_time"] == "" {
		metadataMap["deletion_time"] = time.Time{}
	}

	d, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		DecodeHook: mapstructure.StringToTimeHookFunc(time.RFC3339),
		Result:     &metadata,
	})
	if err != nil {
		return nil, fmt.Errorf("error setting up decoder for API response: %w", err)
	}

	err = d.Decode(metadataMap)
	if err != nil {
		return nil, fmt.Errorf("error decoding metadata from API response into VersionMetadata: %w", err)
	}

	return metadata, nil
}

func extractFullMetadata(secret *Secret) (*KVMetadata, error) {
	var metadata *KVMetadata

	if secret.Data == nil {
		return nil, nil
	}

	if versions, ok := secret.Data["versions"]; ok {
		versionsMap := versions.(map[string]interface{})
		if len(versionsMap) > 0 {
			for version, metadata := range versionsMap {
				metadataMap := metadata.(map[string]interface{})
				// deletion_time usually comes in as an empty string which can't be
				// processed as time.RFC3339, so we reset it to a convertible value
				if metadataMap["deletion_time"] == "" {
					metadataMap["deletion_time"] = time.Time{}
				}
				versionInt, err := strconv.Atoi(version)
				if err != nil {
					return nil, fmt.Errorf("error converting version %s to integer: %w", version, err)
				}
				metadataMap["version"] = versionInt
				versionsMap[version] = metadataMap // save the updated copy of the metadata map
			}
		}
		secret.Data["versions"] = versionsMap // save the updated copy of the versions map
	}

	d, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeHookFunc(time.RFC3339),
			mapstructure.StringToTimeDurationHookFunc(),
		),
		Result: &metadata,
	})
	if err != nil {
		return nil, fmt.Errorf("error setting up decoder for API response: %w", err)
	}

	err = d.Decode(secret.Data)
	if err != nil {
		return nil, fmt.Errorf("error decoding metadata from API response into KVMetadata: %w", err)
	}

	return metadata, nil
}

func validateRollbackVersion(rollbackVersion *KVSecret) error {
	// Make sure a value already exists
	if rollbackVersion == nil || rollbackVersion.Data == nil {
		return fmt.Errorf("no secret found")
	}

	// Verify metadata found
	if rollbackVersion.VersionMetadata == nil {
		return fmt.Errorf("no version metadata found; rollback only works on existing data")
	}

	// Verify it hasn't been deleted
	if !rollbackVersion.VersionMetadata.DeletionTime.IsZero() {
		return fmt.Errorf("cannot roll back to a version that has been deleted")
	}

	if rollbackVersion.VersionMetadata.Destroyed {
		return fmt.Errorf("cannot roll back to a version that has been destroyed")
	}

	// Verify old data found
	if rollbackVersion.Data == nil {
		return fmt.Errorf("no data found; rollback only works on existing data")
	}

	return nil
}

func mergePatch(ctx context.Context, client *Client, mountPath string, secretPath string, newData map[string]interface{}, opts ...KVOption) (*KVSecret, error) {
	pathToMergePatch := fmt.Sprintf("%s/data/%s", mountPath, secretPath)

	// take any other additional options provided
	// and pass them along to the patch request
	wrappedData := map[string]interface{}{
		"data": newData,
	}
	options := make(map[string]interface{})
	for _, opt := range opts {
		k, v := opt()
		options[k] = v
	}
	if len(opts) > 0 {
		wrappedData["options"] = options
	}

	secret, err := client.Logical().JSONMergePatch(ctx, pathToMergePatch, wrappedData)
	if err != nil {
		// If it's a 405, that probably means the server is running a pre-1.9
		// Vault version that doesn't support the HTTP PATCH method.
		// Fall back to the old way of doing it.
		if re, ok := err.(*ResponseError); ok && re.StatusCode == 405 {
			return readThenWrite(ctx, client, mountPath, secretPath, newData)
		}

		if re, ok := err.(*ResponseError); ok && re.StatusCode == 403 {
			return nil, fmt.Errorf("received 403 from Vault server; please ensure that token's policy has \"patch\" capability: %w", err)
		}

		return nil, fmt.Errorf("error performing merge patch to %s: %s", pathToMergePatch, err)
	}

	metadata, err := extractVersionMetadata(secret)
	if err != nil {
		return nil, fmt.Errorf("secret was written successfully, but unable to view version metadata from response: %w", err)
	}

	kvSecret := &KVSecret{
		Data:            nil, // secret.Data in this case is the metadata
		VersionMetadata: metadata,
		Raw:             secret,
	}

	cm, err := extractCustomMetadata(secret)
	if err != nil {
		return nil, fmt.Errorf("error reading custom metadata for secret %s: %w", secretPath, err)
	}
	kvSecret.CustomMetadata = cm

	return kvSecret, nil
}

func readThenWrite(ctx context.Context, client *Client, mountPath string, secretPath string, newData map[string]interface{}) (*KVSecret, error) {
	// First, read the secret.
	existingVersion, err := client.KVv2(mountPath).Get(ctx, secretPath)
	if err != nil {
		return nil, fmt.Errorf("error reading secret as part of read-then-write patch operation: %w", err)
	}

	// Make sure the secret already exists
	if existingVersion == nil || existingVersion.Data == nil {
		return nil, fmt.Errorf("no existing secret was found at %s when doing read-then-write patch operation: %w", secretPath, err)
	}

	// Verify existing secret has metadata
	if existingVersion.VersionMetadata == nil {
		return nil, fmt.Errorf("no metadata found at %s; patch can only be used on existing data", secretPath)
	}

	// Copy new data over with existing data
	combinedData := existingVersion.Data
	for k, v := range newData {
		combinedData[k] = v
	}

	updatedSecret, err := client.KVv2(mountPath).Put(ctx, secretPath, combinedData, WithCheckAndSet(existingVersion.VersionMetadata.Version))
	if err != nil {
		return nil, fmt.Errorf("error writing secret to %s: %w", secretPath, err)
	}

	return updatedSecret, nil
}
