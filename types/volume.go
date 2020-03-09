package types

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// VolumeBinding src:dst:flags:size
type VolumeBinding struct {
	Source      string
	Destination string
	Flags       string
	SizeInBytes int64
}

// NewVolumeBinding returns pointer of VolumeBinding
func NewVolumeBinding(volume string) (_ *VolumeBinding, err error) {
	var src, dst, flags string
	var size int64

	parts := strings.Split(volume, ":")
	switch len(parts) {
	case 2:
		src, dst = parts[0], parts[1]
	case 3:
		src, dst, flags = parts[0], parts[1], parts[2]
	case 4:
		src, dst, flags = parts[0], parts[1], parts[2]
		if size, err = strconv.ParseInt(parts[3], 10, 64); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("invalid volume: %v", volume)
	}

	vb := &VolumeBinding{
		Source:      src,
		Destination: dst,
		Flags:       flags,
		SizeInBytes: size,
	}
	return vb, vb.Validate()
}

// Validate return error if invalid
func (vb VolumeBinding) Validate() error {
	if vb.Destination == "" {
		return fmt.Errorf("invalid volume, dest must be provided")
	}
	if vb.RequireMonopoly() && vb.SizeInBytes == 0 {
		return fmt.Errorf("invalid volume, size must be provided for monopoly schedule: %v", vb)
	}
	return nil
}

// RequireSchedule returns true if volume binding requires schedule
func (vb VolumeBinding) RequireSchedule() bool {
	return strings.HasSuffix(vb.Source, AUTO) && vb.Flags != ""
}

func (vb VolumeBinding) RequireInfinity() bool {
	return strings.HasSuffix(vb.Source, AUTO) && !strings.Contains(vb.Flags, "m") && vb.SizeInBytes == 0
}

// RequireMonopoly returns true if volume binding requires monopoly schedule
func (vb VolumeBinding) RequireMonopoly() bool {
	return strings.HasSuffix(vb.Source, AUTO) && strings.Contains(vb.Flags, "m")
}

// ToString returns volume string
func (vb VolumeBinding) ToString(normalize bool) (volume string) {
	flags := vb.Flags
	if normalize {
		flags = strings.ReplaceAll(flags, "m", "")
	}

	switch {
	case vb.Flags == "" && vb.SizeInBytes == 0:
		volume = fmt.Sprintf("%s:%s", vb.Source, vb.Destination)
	case vb.SizeInBytes == 0:
		volume = fmt.Sprintf("%s:%s:%s", vb.Source, vb.Destination, flags)
	default:
		volume = fmt.Sprintf("%s:%s:%s:%d", vb.Source, vb.Destination, flags, vb.SizeInBytes)
	}
	return volume
}

// VolumeBindings is a collection of VolumeBinding
type VolumeBindings []*VolumeBinding

// MakeVolumeBindings return VolumeBindings of reference type
func MakeVolumeBindings(volumes []string) (volumeBindings VolumeBindings, err error) {
	for _, vb := range volumes {
		volumeBinding, err := NewVolumeBinding(vb)
		if err != nil {
			return nil, err
		}
		volumeBindings = append(volumeBindings, volumeBinding)
	}
	return
}

// ToStringSlice converts VolumeBindings into string slice
func (vbs VolumeBindings) ToStringSlice(sorted, normalize bool) (volumes []string) {
	if sorted {
		sort.Slice(vbs, func(i, j int) bool { return vbs[i].ToString(false) < vbs[j].ToString(false) })
	}
	for _, vb := range vbs {
		volumes = append(volumes, vb.ToString(normalize))
	}
	return
}

// UnmarshalJSON is used for encoding/json.Unmarshal
func (vbs *VolumeBindings) UnmarshalJSON(b []byte) (err error) {
	volumes := []string{}
	if err = json.Unmarshal(b, &volumes); err != nil {
		return err
	}
	*vbs, err = MakeVolumeBindings(volumes)
	return
}

// MarshalJSON is used for encoding/json.Marshal
func (vbs VolumeBindings) MarshalJSON() ([]byte, error) {
	volumes := []string{}
	for _, vb := range vbs {
		volumes = append(volumes, vb.ToString(false))
	}
	return json.Marshal(volumes)
}

// AdditionalStorage is used for another kind of resource: storage
func (vbs VolumeBindings) AdditionalStorage() (storage int64) {
	for _, vb := range vbs {
		if !vb.RequireSchedule() {
			storage += vb.SizeInBytes
		}
	}
	return
}

// ApplyPlan creates new VolumeBindings according to volume plan
func (vbs VolumeBindings) ApplyPlan(plan VolumePlan) (res VolumeBindings) {
	for _, vb := range vbs {
		newVb := &VolumeBinding{vb.Source, vb.Destination, vb.Flags, vb.SizeInBytes}
		if vmap := plan.GetVolumeMap(vb); vmap != nil {
			newVb.Source = vmap.GetResourceID()
		}
		res = append(res, newVb)
	}
	return
}

// Merge combines two VolumeBindings
func (vbs VolumeBindings) Merge(vbs2 VolumeBindings) (softVolumes VolumeBindings, hardVolumes VolumeBindings, err error) {
	sizeMap := map[[3]string]int64{} // {["AUTO", "/data", "rw"]: 100}
	for _, vb := range append(vbs, vbs2...) {
		if !vb.RequireSchedule() {
			hardVolumes = append(hardVolumes, vb)
			continue
		}
		key := [3]string{vb.Source, vb.Destination, vb.Flags}
		if _, ok := sizeMap[key]; ok {
			sizeMap[key] += vb.SizeInBytes
		} else {
			sizeMap[key] = vb.SizeInBytes
		}
	}

	for key, size := range sizeMap {
		if size < 0 {
			continue
		}
		softVolumes = append(softVolumes, &VolumeBinding{key[0], key[1], key[2], size})
	}
	return
}

// IsEqual return true is two VolumeBindings have the same value
func (vbs VolumeBindings) IsEqual(vbs2 VolumeBindings) bool {
	return reflect.DeepEqual(vbs.ToStringSlice(true, false), vbs2.ToStringSlice(true, false))
}
