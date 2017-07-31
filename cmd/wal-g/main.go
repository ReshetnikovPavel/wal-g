package main

import (
	"flag"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/katie31/wal-g"
	"os"
	"path/filepath"
	"time"
)

var help bool
var helpMsg = "backup-fetch\tfetch a backup from S3\n" +
	"backup-push\tstarts and uploads a finished backup to S3\n" +
	"wal-fetch\tfetch a WAL file from S3\n" +
	"wal-push\tupload a WAL file to S3\n"

func init() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of WAL-G:\n")
		fmt.Fprintf(os.Stderr, "%s", helpMsg)
		flag.PrintDefaults()
	}
}

func main() {
	/**
	 *  Configure and start session with bucket, region, and path names. Checks that environment variables
	 *  are properly set.
	 */
	flag.Parse()
	all := flag.Args()
	if len(all) == 0 {
		fmt.Println("Please choose a command:")
		fmt.Println(helpMsg)
		os.Exit(1)
	}
	command := all[0]
	dirArc := all[1]

	var backupName string
	if len(all) == 3 {
		backupName = all[2]
	}

	tu, pre, err := walg.Configure()

	/***************************************** PANIC or exit? *****************************************/
	if serr, ok := err.(*walg.UnsetEnvVarError); ok {
		fmt.Println(serr.Error())
		os.Exit(1)
	} else if err != nil {
		panic(err)
	}

	fmt.Println("BUCKET:", *pre.Bucket)
	fmt.Println("PATH:", *pre.Server)

	/*** OPTION: BACKUP-FETCH ***/
	if command == "backup-fetch" {
		var allKeys []string
		var keys []string
		var bk *walg.Backup
		/*** Check if BACKUPNAME exists and if it does extract to DIRARC. ***/
		if backupName != "LATEST" {
			bk = &walg.Backup{
				Prefix: pre,
				Path:   aws.String(*pre.Server + "/basebackups_005/"),
				Name:   aws.String(backupName),
			}

			bk.Js = aws.String(*bk.Path + *bk.Name + "_backup_stop_sentinel.json")

			fmt.Println("NEWDIR:", dirArc)
			fmt.Println("PATH:", *bk.Path)
			fmt.Println("NAME:", *bk.Name)
			fmt.Println("JSON:", *bk.Js)
			fmt.Println(bk.CheckExistence())

			if bk.CheckExistence() {
				allKeys = bk.GetKeys()
				keys = allKeys[:len(allKeys)-1]

			} else {
				fmt.Printf("Backup '%s' does not exist.\n", *bk.Name)
				os.Exit(1)
			}

			/*** Find the LATEST valid backup (checks against JSON file and grabs name from there) and extract to DIRARC. ***/
		} else if backupName == "LATEST" {
			bk = &walg.Backup{
				Prefix: pre,
				Path:   aws.String(*pre.Server + "/basebackups_005/"),
			}

			bk.Name = aws.String(bk.GetLatest())
			allKeys = bk.GetKeys()
			keys = allKeys[:len(allKeys)-1]

			fmt.Println("NEWDIR", dirArc)
			fmt.Println("PATH:", *bk.Path)
			fmt.Println("NAME:", *bk.Name)

		}

		f := &walg.FileTarInterpreter{
			NewDir: dirArc,
		}

		out := make([]walg.ReaderMaker, len(keys))
		for i, key := range keys {
			s := &walg.S3ReaderMaker{
				Backup:     bk,
				Key:        aws.String(key),
				FileFormat: walg.CheckType(key),
			}
			out[i] = s
		}

		/*** Extract all except pg_control. ***/
		err = walg.ExtractAll(f, out)
		if serr, ok := err.(*walg.UnsupportedFileTypeError); ok {
			fmt.Println(serr.Error())
			os.Exit(1)
		} else if err != nil {
			panic(err)
		}

		/*** Extract pg_control last. If pg_control does not exist, program exits with error code 1. ***/
		name := *bk.Path + *bk.Name + "/tar_partitions/pg_control.tar.lz4"
		pgControl := &walg.Archive{
			Prefix:  pre,
			Archive: aws.String(name),
		}

		if pgControl.CheckExistence() {
			sentinel := make([]walg.ReaderMaker, 1)
			sentinel[0] = &walg.S3ReaderMaker{
				Backup:     bk,
				Key:        aws.String(name),
				FileFormat: walg.CheckType(name),
			}
			err := walg.ExtractAll(f, sentinel)
			if serr, ok := err.(*walg.UnsupportedFileTypeError); ok {
				fmt.Println(serr.Error())
				os.Exit(1)
			} else if err != nil {
				panic(err)
			}
		} else {
			fmt.Println("Corrupt backup: missing pg_control")
			os.Exit(1)
		}
	} else if command == "wal-fetch" {
		/*** Fetch and decompress a WAL file from S3. ***/
		a := &walg.Archive{
			Prefix:  pre,
			Archive: aws.String(*pre.Server + "/wal_005/" + dirArc + ".lzo"),
		}

		if a.CheckExistence() {
			arch := a.GetArchive()
			f, err := os.Create(backupName)
			if err != nil {
				panic(err)
			}

			walg.DecompressLzo(f, arch)
			f.Close()
		} else if a.Archive = aws.String(*pre.Server + "/wal_005/" + dirArc + ".lz4"); a.CheckExistence() {
			arch := a.GetArchive()
			f, err := os.Create(backupName)
			if err != nil {
				panic(err)
			}

			walg.DecompressLz4(f, arch)
			f.Close()
		} else {
			fmt.Printf("Archive '%s' does not exist.\n", dirArc)
			os.Exit(1)
		}

	} else if command == "wal-push" {
		_ = tu.UploadWal(dirArc)
		tu.Finish()
		if err != nil {
			panic(err)
		}
	} else if command == "backup-push" {
		bundle := &walg.Bundle{
			MinSize: int64(1000000000), //MINSIZE = 1GB
		}
		c, err := walg.Connect()
		if err != nil {
			panic(err)
		}
		lbl, sc := walg.QueryFile(c, time.Now().String())
		n, err := walg.FormatName(lbl)
		if err != nil {
			panic(err)
		}

		bundle.Tbm = &walg.S3TarBallMaker{
			BaseDir:  filepath.Base(dirArc),
			Trim:     dirArc,
			BkupName: n,
			Tu:       tu,
		}

		/*** WALK the DIRARC directory and upload to S3. ***/
		bundle.NewTarBall()
		fmt.Println("Walking ...")
		err = filepath.Walk(dirArc, bundle.TarWalker)
		if err != nil {
			panic(err)
		}
		err = bundle.Tb.CloseTar()
		if err != nil {
			panic(err)
		}

		/*** UPLOAD label files. ***/
		bundle.HandleLabelFiles(lbl, sc)

		/*** UPLOAD `pg_control`. ***/
		bundle.HandleSentinel()
		err = bundle.Tb.Finish()
		if err != nil {
			panic(err)
		}
	}

}