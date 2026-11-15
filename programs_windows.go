//go:build windows

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

func regOpen(root syscall.Handle, path string) (syscall.Handle, bool) {
	var h syscall.Handle
	if syscall.RegOpenKeyEx(root, wstr(path), 0, syscall.KEY_READ, &h) != nil {
		return 0, false
	}
	return h, true
}

func regSubkeys(h syscall.Handle) []string {
	var n, maxLen uint32
	if syscall.RegQueryInfoKey(h, nil, nil, nil, &n, &maxLen, nil, nil, nil, nil, nil, nil) != nil {
		return nil
	}
	buf := make([]uint16, maxLen+1)
	var out []string
	for i := uint32(0); i < n; i++ {
		l := uint32(len(buf))
		if syscall.RegEnumKeyEx(h, i, &buf[0], &l, nil, nil, nil, nil) == nil {
			out = append(out, syscall.UTF16ToString(buf[:l]))
		}
	}
	return out
}

func regValue(h syscall.Handle, name string) (uint32, []byte) {
	var typ, n uint32
	if syscall.RegQueryValueEx(h, wstr(name), nil, &typ, nil, &n) != nil || n == 0 {
		return 0, nil
	}
	buf := make([]byte, n+2)
	if syscall.RegQueryValueEx(h, wstr(name), nil, &typ, &buf[0], &n) != nil {
		return 0, nil
	}
	return typ, buf[:n]
}

func regString(h syscall.Handle, name string) string {
	typ, b := regValue(h, name)
	if typ != syscall.REG_SZ && typ != syscall.REG_EXPAND_SZ || len(b) < 2 {
		return ""
	}
	u := unsafe.Slice((*uint16)(unsafe.Pointer(&b[0])), len(b)/2)
	s := strings.TrimSpace(syscall.UTF16ToString(u))
	if typ == syscall.REG_EXPAND_SZ {
		s = os.ExpandEnv(strings.NewReplacer("%", "$").Replace(s)) // %VAR% -> $VAR
	}
	return s
}

func regDWORD(h syscall.Handle, name string) (uint32, bool) {
	typ, b := regValue(h, name)
	if typ != syscall.REG_DWORD || len(b) < 4 {
		return 0, false
	}
	return le32(b), true
}

// listPrograms lee los programas instalados del registro de Windows y
// mide lo que ocupa la carpeta de cada uno.
func listPrograms(progress func(done, total int)) ([]Program, error) {
	type src struct {
		root syscall.Handle
		path string
	}
	srcs := []src{
		{syscall.HKEY_LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`},
		{syscall.HKEY_LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`},
		{syscall.HKEY_CURRENT_USER, `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`},
	}
	seen := map[string]int{}
	var ps []Program
	for _, s := range srcs {
		h, ok := regOpen(s.root, s.path)
		if !ok {
			continue
		}
		for _, k := range regSubkeys(h) {
			kh, ok := regOpen(h, k)
			if !ok {
				continue
			}
			name := regString(kh, "DisplayName")
			sysComp, _ := regDWORD(kh, "SystemComponent")
			parent := regString(kh, "ParentKeyName")
			rel := strings.ToLower(regString(kh, "ReleaseType"))
			if name == "" || sysComp == 1 || parent != "" || strings.Contains(rel, "update") || rel == "hotfix" {
				syscall.RegCloseKey(kh)
				continue
			}
			p := Program{Name: name, Publisher: regString(kh, "Publisher"), Version: regString(kh, "DisplayVersion"),
				Location: strings.Trim(regString(kh, "InstallLocation"), `"`), Installed: regString(kh, "InstallDate")}
			if kb, ok := regDWORD(kh, "EstimatedSize"); ok && kb > 0 {
				p.Size, p.Estimated = int64(kb)*1024, true
			}
			syscall.RegCloseKey(kh)
			key := strings.ToLower(p.Name + "|" + p.Version)
			if i, dup := seen[key]; dup {
				if p.Size > ps[i].Size {
					ps[i] = p
				}
				continue
			}
			seen[key] = len(ps)
			ps = append(ps, p)
		}
		syscall.RegCloseKey(h)
	}
	// Medir la carpeta real (el tamaño que declara el instalador suele
	// estar desfasado). Se evitan carpetas generales como «Program Files».
	skip := map[string]bool{}
	for _, v := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "ProgramData", "SystemRoot",
		"windir", "USERPROFILE", "LOCALAPPDATA", "APPDATA", "PUBLIC", "SystemDrive"} {
		if p := os.Getenv(v); p != "" {
			skip[strings.ToLower(filepath.Clean(p))] = true
		}
	}
	measured := map[string]int64{}
	for i := range ps {
		if progress != nil {
			progress(i, len(ps))
		}
		loc := ps[i].Location
		if loc == "" {
			continue
		}
		clean := strings.ToLower(filepath.Clean(loc))
		if skip[clean] || len(filepath.VolumeName(clean))+1 >= len(clean) || strings.HasSuffix(clean, `\users`) {
			continue
		}
		if st, err := os.Stat(loc); err != nil || !st.IsDir() {
			continue
		}
		size, ok := measured[clean]
		if !ok {
			filepath.WalkDir(loc, func(_ string, d fs.DirEntry, err error) error {
				if err == nil && d.Type().IsRegular() {
					if info, err := d.Info(); err == nil {
						size += info.Size()
					}
				}
				return nil
			})
			measured[clean] = size
		}
		if size > 0 {
			ps[i].Size, ps[i].Estimated = size, false
		}
	}
	if progress != nil {
		progress(len(ps), len(ps))
	}
	sortPrograms(ps)
	return ps, nil
}
