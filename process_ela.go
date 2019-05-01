/*
 * Run as: go run process_ela.go file
 */

package main

import (
  "os"
  "fmt"
  "image"
  "bufio"
  "bytes"
  "errors"
  "strings"
  "strconv"
  "context"
  "net/url"
  "net/http"
  "image/png"
  "image/gif"
  "image/jpeg"
  "image/color"
  "path/filepath"

  "github.com/aws/aws-sdk-go/aws"
  "github.com/aws/aws-lambda-go/lambda"
  "github.com/aws/aws-lambda-go/events"
  "github.com/aws/aws-sdk-go/service/s3"
  "github.com/aws/aws-sdk-go/aws/session"
  "github.com/aws/aws-sdk-go/service/s3/s3manager"

  pdf "github.com/unidoc/unidoc/pdf/model"
  pdfcore "github.com/unidoc/unidoc/pdf/core"
  pdfcontent "github.com/unidoc/unidoc/pdf/contentstream"
)

var (
  ps_id = ""
  tmppath = ""
  bucket = ""
  itempath = ""
  filename = ""
  inlineImages = 0
  filename_ela = ""
  itemrootpath = ""
  xObjectImages = 0
  chanceoffraudscore = 0
)

func main() {
  if os.Getenv("AWS_EXECUTION_ENV") != "" { lambda.Start(LambdaHandleRequest)
  } else { CLIHandleRequest() }
}

func LambdaHandleRequest(ctx context.Context, s3Event events.S3Event) (string, error) {
  for _, record := range s3Event.Records {
    s3 := record.S3
    fmt.Printf("[%s - %s] Bucket = %s, File = %s \n", record.EventSource, record.EventTime, s3.Bucket.Name, s3.Object.Key)
    if strings.Contains(s3.Object.Key, "/DirectDepositRequiredFiles/") {
    } else {
      fmt.Printf("DirectDepositRequiredFiles directory not found in path\n")
      return "DirectDepositRequiredFiles directory not found in path\n", nil
    }

    decodedKey, err := url.QueryUnescape(s3.Object.Key)
    if err != nil {
      fmt.Printf("s3 url couldn't be decoded (%s)\n", err.Error())
      return "s3 url couldn't be decoded\n", err
    }


    itemrootpath	= filepath.Dir(decodedKey)
    path		:= "s3://" + s3.Bucket.Name + "/" + decodedKey
    file, err		:= LoadFile(path)
    if err != nil {
      if err.Error() == "File path too long\n" { return "Skipping file", nil }
      fmt.Printf(err.Error())
      return "Error!\n", err
    }
    processFile(file)

    err		= os.Remove(tmppath + filename)
    if err != nil {
      fmt.Printf(err.Error())
      return "Error!\n", err
    }
  }


  if xObjectImages + inlineImages == 0 { return "No files to upload\n", nil }
  var files []string
  err	:= filepath.Walk(tmppath, func(path string, info os.FileInfo, err error) error {
    files = append(files, path)
    return nil
  })
  if err != nil { return "Couldn't get list of files to upload", err }
  for _, currentfile := range files {
    if currentfile == tmppath { continue }
    basefilename	:= filepath.Base(currentfile)
    err		= PutFileS3(bucket, itemrootpath + "/ELA/" + basefilename, currentfile)
    if err != nil {
      fmt.Printf("File didn't upload to S3 %s (%s)\n", currentfile, err.Error())
    }

    fmt.Printf("File uploaded (%s)\n", currentfile)
    err		= os.Remove(currentfile)
    if err != nil { fmt.Printf("File didn't delete (%s)\n", currentfile) }
  }

  err	= os.Remove(tmppath)
  if err != nil {
    fmt.Printf(err.Error())
    return "Couldn't delete tmppath (" + tmppath + ")\n", nil
  }
  return "All files processed\n", nil
}

func CLIHandleRequest() {
  if len(os.Args) < 2 {
    fmt.Printf("Syntax: go run process_ela.go file.ext\n")
    os.Exit(1)
  }

  inputPath := os.Args[1]

  fmt.Printf("Input file: %s\n", inputPath)
  file, err := LoadFile(inputPath)
  if err != nil {
    fmt.Printf(err.Error())
    os.Exit(1)
  }
  processFile(file)
}

func LoadFile(path string) (*os.File, error) {
  var file *os.File
  var err error
  if string(path[0:5]) == "s3://" {
    fmt.Printf("S3 file\n")
    patharr	:= strings.Split(path, "/")
    if len(patharr) > 7 { return file, errors.New("File path too long\n") }
    ps_id	= patharr[4]
    filename	= patharr[6]
    bucket	= patharr[2]
    itempath	= strings.Replace(path, "s3://" + bucket + "/", "", 1)
    tmppath	= "/tmp/" + ps_id + "/"

    err		= os.Mkdir(tmppath, 0777)
    if err != nil {
      fmt.Printf("Temp folder failed to create (%s) (%s)\n", tmppath, err.Error())
      return file, err
    }

    err		= GetFileS3(bucket, itempath, filename)
    if err != nil {
      fmt.Printf("File didn't download from S3 %s - Bucket: %s - ItemPath: %s (%s)\n", path, bucket, itempath, err.Error())
      return file, err
    }
    path	= tmppath + filename
  } else { fmt.Printf("Local file\n") }

  file, err = os.Open(path)
  if err != nil {
    fmt.Printf("Couldn't open file %s (%s)\n", path, err.Error())
    return file, errors.New("Couldn't open file " + path + " (" + err.Error() + ")\n")
  }

  fileinfo, err := file.Stat()
  if err != nil {
    fmt.Printf("Couldn't read file %s stats (%s)\n", path, err.Error())
    return file, err
  }

  filename = fileinfo.Name()
  fileext := filepath.Ext(filename)
  if fileext == "" { filename_ela = tmppath + filename + "_ela"
  } else { filename_ela = tmppath + strings.Replace(filename, fileext, "", 1) + "_ela" + ".jpg" }

  return file, nil
}

func processFile(file *os.File) {
  buff := make([]byte, 512)
  _, err := file.Read(buff)
  if err != nil {
    fmt.Printf("Couldn't read file (%s) to detect filetype (%s)\n", filename, err.Error())
    os.Exit(1)
  }
  filetype := http.DetectContentType(buff)
  fmt.Printf("Content Type: %s\n", filetype)
  file.Seek(0, 0)
  var img image.Image

  switch filetype {
    case "application/pdf":
      err := extractImagesFromPDF(file)
      if err != nil {
        fmt.Printf("Error: %s\n", err.Error())
        os.Exit(1)
      }
    case "image/jpeg", "image/jpg":
      img, err = jpeg.Decode(file)
      if err != nil {
        fmt.Printf("Couldn't decode jpeg (%s) (%s)\n", filename, err.Error())
        os.Exit(1)
      }
      processImage(img, filename_ela)
      inlineImages++
    case "image/png":
      img, err = png.Decode(file)
      if err != nil {
        fmt.Printf("Couldn't decode jpeg (%s) (%s)\n", filename, err.Error())
        os.Exit(1)
      }
      processImage(img, filename_ela)
      inlineImages++
    case "image/gif":
      img, err = gif.Decode(file)
      if err != nil {
        fmt.Printf("Couldn't decode jpeg (%s) (%s)\n", filename, err.Error())
        os.Exit(1)
      }
      processImage(img, filename_ela)
      inlineImages++
    default:
      fmt.Printf("Error: %s filetype not supported", filetype)
      os.Exit(1)
  }

  img = nil
  file.Close()

  chanceoffraud := "low"
  switch {
    case chanceoffraudscore > 0 && chanceoffraudscore <= 1:
      chanceoffraud = "medium"
    case chanceoffraudscore > 1 && chanceoffraudscore <= 2:
      chanceoffraud = "high"
    case chanceoffraudscore > 2 && chanceoffraudscore <= 3:
      chanceoffraud = "very high"
    case chanceoffraudscore > 3:
      chanceoffraud = "very very high"
  }

  fmt.Printf("-- Summary\n")
  fmt.Printf("Total %d images\n", xObjectImages + inlineImages)
  fmt.Printf("Risk: %s\n", chanceoffraud)
}

func extractImagesFromPDF(file *os.File) error {
  pdfReader, err := pdf.NewPdfReader(file)
  if err != nil { return err }

  isEncrypted, err := pdfReader.IsEncrypted()
  if err != nil { return err }

  // Try decrypting with an empty one.
  if isEncrypted {
    auth, err := pdfReader.Decrypt([]byte(""))
    if err != nil { return err }
    if !auth {
      fmt.Println("Need to decrypt with password")
      return nil
    }
  }

  numPages, err := pdfReader.GetNumPages()
  if err != nil { return err }

  trailerDict, err := pdfReader.GetTrailer()
  if err != nil { fmt.Println("Couldn't read Metadata trailer. (%s)\n", err.Error())
  } else {
    if trailerDict == nil { fmt.Printf("Metadata trailer is empty\n")
    } else {
      var infoDict *pdfcore.PdfObjectDictionary
      infoObj := trailerDict.Get("Info")
      switch t := infoObj.(type) {
        case *pdfcore.PdfObjectReference:
          infoRef := t
          infoObj, err = pdfReader.GetIndirectObjectByNumber(int(infoRef.ObjectNumber))
          infoObj = pdfcore.TraceToDirectObject(infoObj)
          if err != nil { fmt.Printf("Reading Metadata trailer failed (%s)\n", err.Error())
          } else { infoDict, _ = infoObj.(*pdfcore.PdfObjectDictionary) }
	case *pdfcore.PdfObjectDictionary:
          infoDict = t
      }

      if infoDict == nil { fmt.Printf("Metadata dictionary not present\n")
      } else {
	di := pdfDocInfo{
          Filename: filename,
          NumPages: numPages,
	}

        if str, has := infoDict.Get("Title").(*pdfcore.PdfObjectString); has { di.Title = str.String() }
        if str, has := infoDict.Get("Author").(*pdfcore.PdfObjectString); has { di.Author = str.String() }
        if str, has := infoDict.Get("Keywords").(*pdfcore.PdfObjectString); has { di.Keywords = str.String() }
        if str, has := infoDict.Get("Creator").(*pdfcore.PdfObjectString); has { di.Creator = str.String() }
        if str, has := infoDict.Get("Producer").(*pdfcore.PdfObjectString); has { di.Producer = str.String() }
        if str, has := infoDict.Get("CreationDate").(*pdfcore.PdfObjectString); has { di.CreationDate = str.String() }
        if str, has := infoDict.Get("ModDate").(*pdfcore.PdfObjectString); has { di.ModDate = str.String() }
        if name, has := infoDict.Get("Trapped").(*pdfcore.PdfObjectName); has { di.Trapped = name.String() }

        di.print()
      }
    }
  }

  fmt.Printf("PDF Num Pages: %d\n", numPages)
  basefilename	:= ""
  fileext	:= filepath.Ext(filename)
  if fileext == "" { basefilename = filename
  } else { basefilename = strings.Replace(filename, fileext, "", 1) }

  var rgbImages []*pdf.Image
  for i := 0; i < numPages; i++ {
    fmt.Printf("-----\nPage %d:\n", i+1)

    page, err := pdfReader.GetPage(i + 1)
    if err != nil { return err }

    rgbImages, err = extractImagesOnPage(page)
    if err != nil {
      fmt.Printf(" Unsupport image (%v)\n", err.Error())
      continue
    }

    fname	:= ""
    fname_ela	:= ""
    var gimg image.Image
    opt		:= jpeg.Options{Quality: 100}
    for idx, img := range rgbImages {
      fname	= fmt.Sprintf("%s_p%d_%d.jpg", tmppath + basefilename, i+1, idx)
      fname_ela	= fmt.Sprintf("%s_p%d_%d_ela.jpg", tmppath + basefilename, i+1, idx)

      gimg, err = img.ToGoImage()
      if err != nil {
        fmt.Printf(" Couldn't read image (%v)\n", err.Error())
	continue
      }

      file, err := os.Create(fname)
      if err != nil {
        fmt.Printf(" Couldn't create file (%v)\n", err.Error())
        continue
      }

      err = jpeg.Encode(file, gimg, &opt)
      file.Close()
      if err != nil {
        fmt.Printf(" Couldn't save 100% jpeg file (%v)\n", err.Error())
        continue
      }
      processImage(gimg, fname_ela)
      gimg = nil
    }
    page = nil
  }
  rgbImages = nil
  pdfReader = nil
  return nil
}

func processImage(img image.Image, filename string) {
  var buff80 bytes.Buffer
  buff80W := bufio.NewWriter(&buff80)
  opt := jpeg.Options{Quality: 80}
  err := jpeg.Encode(buff80W, img, &opt)
  buff80W.Flush()
  if err != nil {
    fmt.Printf(" Couldn't 80% jpeg to buffer (%v)\n", err.Error())
    return
  }

  buff80R := bufio.NewReader(&buff80)
  img80, err := jpeg.Decode(buff80R)
  if err != nil {
    fmt.Printf(" Couldn't decode 80% jpeg buffer (%v)\n", err.Error())
    return
  }
  buff80.Reset()

  bounds := img.Bounds()
  if !bounds.Eq(img80.Bounds()) {
    fmt.Printf(" PDF image not the same size of 80% jpeg file (%v)\n", err.Error())
    return
  }

  var sum int64
  var diffcolor color.RGBA
  var r1, r2, g1, g2, b1, b2 uint32
  var diffred, diffgreen, diffblue uint8
  diffimg := image.NewRGBA(image.Rect(0, 0, int(bounds.Max.X), int(bounds.Max.Y)))
  for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
    for x := bounds.Min.X; x < bounds.Max.X; x++ {
      r1, g1, b1, _ = img.At(x, y).RGBA()
      r2, g2, b2, _ = img80.At(x, y).RGBA()
      diffred	= diff(r1, r2)
      diffgreen	= diff(g1, g2)
      diffblue	= diff(b1, b2)
      sum	+= int64(diffred + diffgreen + diffblue)
      diffcolor	= color.RGBA{R: diffred, G: diffgreen, B: diffblue, A: 0}
      diffimg.SetRGBA(x, y, diffcolor)
    }
  }

  nPixels := (bounds.Max.X - bounds.Min.X) * (bounds.Max.Y - bounds.Min.Y)
  diffscore := float64(sum*100) / (float64(nPixels) * 0xffff * 3)
  fmt.Printf("%s ELA diff: %f%%\n", filename, diffscore)
  switch {
    case diffscore > 0.004: chanceoffraudscore += 6
    case diffscore > 0.003: chanceoffraudscore += 5
    case diffscore > 0.002: chanceoffraudscore += 4
    case diffscore > 0.001: chanceoffraudscore += 3
    case diffscore > 0.0005: chanceoffraudscore += 2
    case diffscore > 0.00025: chanceoffraudscore += 1
  }

  file, err := os.Create(filename)
  if err != nil {
    fmt.Printf(" Couldn't create file (%v)\n", err.Error())
    return
  }

  opt = jpeg.Options{Quality: 100}
  err = jpeg.Encode(file, diffimg, &opt)
  file.Close()
  diffimg = nil
  if err != nil {
    fmt.Printf(" Couldn't save ela jpeg file (%v)\n", err.Error())
    return
  }
}

func diff(a, b uint32) uint8 {
    if a > b { return uint8(a - b) }
    return uint8(b - a)
}

func extractImagesOnPage(page *pdf.PdfPage) ([]*pdf.Image, error) {
  contents, err := page.GetAllContentStreams()
  if err != nil { return nil, err }

  return extractImagesInContentStream(contents, page.Resources)
}

func extractImagesInContentStream(contents string, resources *pdf.PdfPageResources) ([]*pdf.Image, error) {
  rgbImages := []*pdf.Image{}
  cstreamParser := pdfcontent.NewContentStreamParser(contents)
  operations, err := cstreamParser.Parse()
  if err != nil { return nil, err }

  processedXObjects := map[string]bool{}

  // Range through all the content stream operations.
  for _, op := range *operations {
    if op.Operand == "BI" && len(op.Params) == 1 {
      // BI: Inline image.
      iimg, ok := op.Params[0].(*pdfcontent.ContentStreamInlineImage)
      if !ok { continue	}

      img, err := iimg.ToImage(resources)
      if err != nil { return nil, err }

      cs, err := iimg.GetColorSpace(resources)
      if err != nil { return nil, err }

      if cs == nil {
        // Default if not specified?
        cs = pdf.NewPdfColorspaceDeviceGray()
      }

      fmt.Printf("Cs: %T\n", cs)

      rgbImg, err := cs.ImageToRGB(*img)
      if err != nil { return nil, err }

      rgbImages = append(rgbImages, &rgbImg)
      inlineImages++
    } else if op.Operand == "Do" && len(op.Params) == 1 {
      // Do: XObject.
      name := op.Params[0].(*pdfcore.PdfObjectName)

      // Only process each one once.
      _, has := processedXObjects[string(*name)]
      if has { continue }

      processedXObjects[string(*name)] = true

      _, xtype := resources.GetXObjectByName(*name)
      if xtype == pdf.XObjectTypeImage {
        fmt.Printf(" XObject Image: %s\n", *name)

        ximg, err := resources.GetXObjectImageByName(*name)
        if err != nil { return nil, err }

        img, err := ximg.ToImage()
        if err != nil {
          fmt.Printf(" Unsupport image (%v)\n", err.Error())
          continue
          //return nil, err
        }

        rgbImg, err := ximg.ColorSpace.ImageToRGB(*img)
        if err != nil { return nil, err }

        rgbImages = append(rgbImages, &rgbImg)
        xObjectImages++
      } else if xtype == pdf.XObjectTypeForm {
        // Go through the XObject Form content stream.
        xform, err := resources.GetXObjectFormByName(*name)
        if err != nil { return nil, err }

        formContent, err := xform.GetContentStream()
        if err != nil { return nil, err }

        // Process the content stream in the Form object too:
        formResources := xform.Resources
        if formResources == nil { formResources = resources }

        // Process the content stream in the Form object too:
        formRgbImages, err := extractImagesInContentStream(string(formContent), formResources)
        if err != nil { return nil, err }
        rgbImages = append(rgbImages, formRgbImages...)
      }
    }
  }

  return rgbImages, nil
}

func GetFileS3(funcbucket string, funcitempath string, filename string) error {
  file, err	:= os.Create(tmppath + filename)
  if err != nil { return err }

  sess, _	:= session.NewSession(&aws.Config{ Region: aws.String("us-east-1")}, )
  downloader	:= s3manager.NewDownloader(sess)

  _, err = downloader.Download(file,
      &s3.GetObjectInput{
          Bucket:	aws.String(funcbucket),
          Key:		aws.String(funcitempath),
      })
  file.Close()
  return err
}

func PutFileS3(funcbucket string, funcitempath string, currentfile string) error {
  sess, _	:= session.NewSession(&aws.Config{ Region: aws.String("us-east-1")}, )
  uploader	:= s3manager.NewUploader(sess)
  file, err	:= os.Open(currentfile)
  if err != nil { return err }

  _, err = uploader.Upload(&s3manager.UploadInput{
    Bucket:	aws.String(funcbucket),
    Key:	aws.String(funcitempath),
    Body:	file,
  })
  file.Close()
  return err
}

type pdfDocInfo struct {
  Filename	string
  NumPages	int
  Title		string
  Author	string
  Subject	string
  Keywords	string
  Creator	string
  Producer	string
  CreationDate	string
  ModDate	string
  Trapped	string
}

func (di pdfDocInfo) print() {
  fmt.Printf("Filename: %s\n", di.Filename)
  fmt.Printf("  Pages: %d\n", di.NumPages)
  fmt.Printf("  Title: %s\n", di.Title)
  fmt.Printf("  Author: %s\n", di.Author)
  fmt.Printf("  Subject: %s\n", di.Subject)
  fmt.Printf("  Keywords: %s\n", di.Keywords)
  fmt.Printf("  Creator: %s\n", di.Creator)
  fmt.Printf("  Producer: %s\n", di.Producer)
  fmt.Printf("  CreationDate: %s\n", di.CreationDate)
  fmt.Printf("  ModDate: %s\n", di.ModDate)
  fmt.Printf("  Trapped: %s\n", di.Trapped)
  var jsonbuff bytes.Buffer
  jsonbuff.WriteString("{\"filename\":\"")
  jsonbuff.WriteString(di.Filename)
  jsonbuff.WriteString("\",\"pages\":")
  jsonbuff.WriteString(strconv.Itoa(di.NumPages))
  jsonbuff.WriteString(",\"title\":\"")
  jsonbuff.WriteString(di.Title)
  jsonbuff.WriteString("\",\"author\":\"")
  jsonbuff.WriteString(di.Author)
  jsonbuff.WriteString("\",\"subject\":\"")
  jsonbuff.WriteString(di.Subject)
  jsonbuff.WriteString("\",\"keywords\":\"")
  jsonbuff.WriteString(di.Keywords)
  jsonbuff.WriteString("\"creator\":\"")
  jsonbuff.WriteString(di.Creator)
  jsonbuff.WriteString("\"producer\":\"")
  jsonbuff.WriteString(di.Producer)
  jsonbuff.WriteString("\"creationdate\":\"")
  jsonbuff.WriteString(di.CreationDate)
  jsonbuff.WriteString("\"moddate\":\"")
  jsonbuff.WriteString(di.ModDate)
  jsonbuff.WriteString("\"trapped\":\"")
  jsonbuff.WriteString(di.Trapped)

  metafilename := ""
  fileext := filepath.Ext(di.Filename)
  if fileext == "" { metafilename = di.Filename + "_meta.txt"
  } else { metafilename = strings.Replace(di.Filename, fileext, "", 1) + "_meta.txt" }

  metafile, err := os.Create(tmppath + metafilename)
  if err != nil {
    fmt.Printf("Metafile wasn't created (%s)\n", tmppath + metafilename)
  } else {
    _, err := metafile.Write(jsonbuff.Bytes())
    if err != nil { fmt.Printf("Data couldn't be written to metafile (%s)\n", tmppath + metafilename)
    } else { fmt.Printf("Wrote metafile (%s)\n", tmppath + metafilename) }
    metafile.Close()
    xObjectImages++
  }
}
