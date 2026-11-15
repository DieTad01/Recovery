package main

import (
	"bufio"
	"compress/gzip"
	"encoding/gob"
	"fmt"
	"os"
)

// El resultado del escaneo completo (que puede tardar horas) se guarda en
// la carpeta de destino para poder continuar o repetir la recuperación sin
// volver a leer todo el disco.

const indexVersion = 3

type indexFile struct {
	Version   int
	DevSize   int64
	ScannedTo int64
	Opts      ScanOpts
	Recs      []*FileRec
	OldMFT    []*FileRec
	Cands     []Cand
	Boots     []BootHit
}

func saveIndex(path string, s *Scan, recs []*FileRec) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	zw, _ := gzip.NewWriterLevel(bw, gzip.BestSpeed)
	err = gob.NewEncoder(zw).Encode(&indexFile{
		Version: indexVersion, DevSize: s.Dev.Size, ScannedTo: s.ScannedTo, Opts: s.Opts,
		Recs: recs, OldMFT: s.OldMFT, Cands: s.Cands, Boots: s.Boots,
	})
	if e := zw.Close(); err == nil {
		err = e
	}
	if e := bw.Flush(); err == nil {
		err = e
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	os.Remove(path)
	return os.Rename(tmp, path)
}

func loadIndex(path string) (*indexFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(bufio.NewReaderSize(f, 1<<20))
	if err != nil {
		return nil, err
	}
	var idx indexFile
	if err := gob.NewDecoder(zr).Decode(&idx); err != nil {
		return nil, err
	}
	if idx.Version != indexVersion {
		return nil, fmt.Errorf("versión de índice distinta")
	}
	return &idx, nil
}
