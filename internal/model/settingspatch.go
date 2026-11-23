package model

import "reflect"

// MergeSettings applies a change to cur: every option that differs between
// base and next takes next's value, every other option keeps cur's. base is
// the snapshot the writer read and next what it made of it, so a writer
// that changed one option does not undo what others stored since it read.
//
// Options are compared one by one: structs made of exported fields
// (DNSSettings, AdvancedSettings, …) are gone through field by field, so
// two writers changing different options of one section both land. A list,
// a map or a time is one option: of two writers changing the same list the
// later wins.
//
// The result shares no memory with next; what it keeps of cur it shares
// with cur.
func MergeSettings(cur, base, next Settings) Settings {
	mergeValue(reflect.ValueOf(&cur).Elem(), reflect.ValueOf(base), reflect.ValueOf(next))
	return cur
}

// CloneSettings is a deep copy of s: changing a list of the copy in place
// leaves s alone.
func CloneSettings(s Settings) Settings {
	return cloneValue(reflect.ValueOf(s)).Interface().(Settings)
}

// SettingsChanged names the options MergeSettings(cur, base, next) would
// set, as dotted paths ("Mode", "DNS.TakeOver"), in declaration order.
func SettingsChanged(base, next Settings) []string {
	var out []string
	changedPaths(reflect.ValueOf(base), reflect.ValueOf(next), "", &out)
	return out
}

// plainStruct reports whether t is a struct merged field by field: one
// with exported fields only (time.Time is not, and is one value).
func plainStruct(t reflect.Type) bool {
	if t.Kind() != reflect.Struct || t.NumField() == 0 {
		return false
	}
	for i := 0; i < t.NumField(); i++ {
		if !t.Field(i).IsExported() {
			return false
		}
	}
	return true
}

func mergeValue(cur, base, next reflect.Value) {
	if plainStruct(cur.Type()) {
		for i := 0; i < cur.NumField(); i++ {
			mergeValue(cur.Field(i), base.Field(i), next.Field(i))
		}
		return
	}
	if !sameOption(base, next) {
		cur.Set(cloneValue(next))
	}
}

func changedPaths(base, next reflect.Value, path string, out *[]string) {
	if plainStruct(base.Type()) {
		for i := 0; i < base.NumField(); i++ {
			p := base.Type().Field(i).Name
			if path != "" {
				p = path + "." + p
			}
			changedPaths(base.Field(i), next.Field(i), p, out)
		}
		return
	}
	if !sameOption(base, next) {
		*out = append(*out, path)
	}
}

// sameOption compares two values of one option. A nil list and an empty
// one are the same: JSON, the form settings travel and are stored in,
// does not always tell them apart.
func sameOption(a, b reflect.Value) bool {
	switch a.Kind() {
	case reflect.Slice, reflect.Map:
		if a.Len() == 0 && b.Len() == 0 {
			return true
		}
	}
	return reflect.DeepEqual(a.Interface(), b.Interface())
}

func cloneValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		if plainStruct(v.Type()) {
			for i := 0; i < v.NumField(); i++ {
				out.Field(i).Set(cloneValue(v.Field(i)))
			}
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(cloneValue(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		for it := v.MapRange(); it.Next(); {
			out.SetMapIndex(it.Key(), cloneValue(it.Value()))
		}
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(cloneValue(v.Elem()))
		return out
	}
	return v
}

// SettingsPaths lists every option of Settings as MergeSettings sees them,
// as dotted paths.
func SettingsPaths() []string {
	var out []string
	var walk func(t reflect.Type, path string)
	walk = func(t reflect.Type, path string) {
		if !plainStruct(t) {
			out = append(out, path)
			return
		}
		for i := 0; i < t.NumField(); i++ {
			p := t.Field(i).Name
			if path != "" {
				p = path + "." + p
			}
			walk(t.Field(i).Type, p)
		}
	}
	walk(reflect.TypeOf(Settings{}), "")
	return out
}

// RevertSettings prepares taking a write back. wrote is what the settings
// were right after the write, old what they were before it and cur what
// they are now. An option the write changed goes back to old's value only
// while cur still has the value the write gave it: one that was changed
// again since is left alone and named in skipped. only, when not nil,
// limits the write to the options it reports true for (what differs
// elsewhere between old and wrote was not the write's doing).
//
// The result is for UpdateSettings(wrote, next).
func RevertSettings(cur, wrote, old Settings, only func(path string) bool) (next Settings, skipped []string) {
	next = CloneSettings(wrote)
	revertValue(reflect.ValueOf(&next).Elem(), reflect.ValueOf(cur), reflect.ValueOf(wrote), reflect.ValueOf(old), "", only, &skipped)
	return next, skipped
}

func revertValue(next, cur, wrote, old reflect.Value, path string, only func(string) bool, skipped *[]string) {
	if plainStruct(next.Type()) {
		for i := 0; i < next.NumField(); i++ {
			p := next.Type().Field(i).Name
			if path != "" {
				p = path + "." + p
			}
			revertValue(next.Field(i), cur.Field(i), wrote.Field(i), old.Field(i), p, only, skipped)
		}
		return
	}
	switch {
	case sameOption(wrote, old), only != nil && !only(path):
	case !sameOption(cur, wrote):
		*skipped = append(*skipped, path)
	default:
		next.Set(cloneValue(old))
	}
}
