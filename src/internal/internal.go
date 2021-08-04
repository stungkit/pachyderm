package internal

import (
	"bytes"
	"context"

	"github.com/gogo/protobuf/types"
	"google.golang.org/grpc/metadata"

	"github.com/pachyderm/pachyderm/v2/src/internal/errors"
	"github.com/pachyderm/pachyderm/v2/src/internal/ppsconsts"
	"github.com/pachyderm/pachyderm/v2/src/internal/tarutil"
	"github.com/pachyderm/pachyderm/v2/src/pfs"
	"github.com/pachyderm/pachyderm/v2/src/pps"
)

type fakeStream struct {
	ctx context.Context
}

func (f fakeStream) SetHeader(metadata.MD) error  { return nil }
func (f fakeStream) SendHeader(metadata.MD) error { return nil }
func (f fakeStream) SetTrailer(metadata.MD)       {}
func (f fakeStream) Context() context.Context     { return f.ctx }
func (f fakeStream) SendMsg(_ interface{}) error  { return nil }
func (f fakeStream) RecvMsg(_ interface{}) error  { return nil }

func (f *fakeStream) SetContext(ctx context.Context) { f.ctx = ctx }

type repoLister struct {
	fakeStream
	list []*pfs.RepoInfo
}

func (l *repoLister) Send(info *pfs.RepoInfo) error {
	l.list = append(l.list, info)
	return nil
}

func ListRepo(ctx context.Context, server pfs.APIServer, req *pfs.ListRepoRequest) ([]*pfs.RepoInfo, error) {
	var lister repoLister
	lister.SetContext(ctx)
	if err := server.ListRepo(req, &lister); err != nil {
		return nil, err
	}
	return lister.list, nil
}

type pipelineLister struct {
	fakeStream
	list []*pps.PipelineInfo
}

func (l *pipelineLister) Send(info *pps.PipelineInfo) error {
	l.list = append(l.list, info)
	return nil
}

func ListPipeline(ctx context.Context, server pps.APIServer, req *pps.ListPipelineRequest) ([]*pps.PipelineInfo, error) {
	var lister pipelineLister
	lister.SetContext(ctx)
	if err := server.ListPipeline(req, &lister); err != nil {
		return nil, err
	}
	return lister.list, nil
}

type fileGetter struct {
	fakeStream
	buffer bytes.Buffer
}

func (g *fileGetter) Send(bytes *types.BytesValue) error {
	_, err := g.buffer.Write(bytes.Value)
	return err // always nil
}

func GetPipelineDetails(ctx context.Context, apiServer pfs.APIServer, info *pps.PipelineInfo) error {
	var getter fileGetter
	getter.SetContext(ctx)
	if err := apiServer.GetFileTAR(&pfs.GetFileRequest{File: info.SpecCommit.NewFile(ppsconsts.SpecFile)}, &getter); err != nil {
		return err
	}
	var file bytes.Buffer
	if err := tarutil.Iterate(&getter.buffer, func(f tarutil.File) error {
		return f.Content(&file)
	}); err != nil {
		return err
	}

	loadedPipelineInfo := &pps.PipelineInfo{}
	if err := loadedPipelineInfo.Unmarshal(file.Bytes()); err != nil {
		return errors.Wrapf(err, "could not unmarshal PipelineInfo bytes from PFS")
	}
	info.Version = loadedPipelineInfo.Version
	info.Details = loadedPipelineInfo.Details
	return nil
}
