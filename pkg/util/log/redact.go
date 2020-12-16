// Copyright 2020 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package log

import (
	"context"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/cockroachdb/cockroach/pkg/util/encoding/encodingtype"
	"github.com/cockroachdb/cockroach/pkg/util/log/logpb"
	"github.com/cockroachdb/errors"
	"github.com/cockroachdb/logtags"
	"github.com/cockroachdb/redact"
)

// EditSensitiveData describes how the messages in log entries should
// be edited through the API.
type EditSensitiveData int

const (
	// The 4 reference values below require the first bit to be
	// set. This ensures the API is not mistakenly used with an
	// uninitialized mode parameter.
	confValid       = 1
	withKeepMarkers = 2
	withRedaction   = 4

	// WithFlattenedSensitiveData is the log including sensitive data,
	// but markers stripped.
	WithFlattenedSensitiveData EditSensitiveData = confValid
	// WithMarkedSensitiveData is the "raw" log with sensitive data markers included.
	WithMarkedSensitiveData EditSensitiveData = confValid | withKeepMarkers
	// WithoutSensitiveDataNorMarkers is the log with the sensitive data
	// redacted, and markers stripped.
	WithoutSensitiveDataNorMarkers EditSensitiveData = confValid | withRedaction
	// WithoutSensitiveData is the log with the sensitive data redacted,
	// but markers included.
	WithoutSensitiveData EditSensitiveData = confValid | withKeepMarkers | withRedaction
)

// KeepRedactable can be used as an argument to SelectEditMode to indicate that
// the logs should retain their sensitive data markers so that they can be
// redacted later.
const KeepRedactable = true

// SelectEditMode returns an EditSensitiveData value that's suitable
// for use with NewDecoder depending on client-side desired
// "redact" and "keep redactable" flags.
// (See the documentation for the Logs and LogFile RPCs
// and that of the 'merge-logs' CLI command.)
func SelectEditMode(redact, keepRedactable bool) EditSensitiveData {
	var editMode EditSensitiveData
	if redact {
		editMode = editMode | withRedaction
	}
	if keepRedactable {
		editMode = editMode | withKeepMarkers
	}
	editMode = editMode | confValid
	return editMode
}

type redactEditor func(redactablePackage) redactablePackage

func getEditor(editMode EditSensitiveData) redactEditor {
	switch editMode {
	case WithMarkedSensitiveData:
		return func(r redactablePackage) redactablePackage {
			if !r.redactable {
				r.msg = []byte(redact.EscapeBytes(r.msg))
				r.redactable = true
			}
			return r
		}
	case WithFlattenedSensitiveData:
		return func(r redactablePackage) redactablePackage {
			if r.redactable {
				r.msg = redact.RedactableBytes(r.msg).StripMarkers()
				r.redactable = false
			}
			return r
		}
	case WithoutSensitiveData:
		return func(r redactablePackage) redactablePackage {
			if r.redactable {
				r.msg = []byte(redact.RedactableBytes(r.msg).Redact())
			} else {
				r.msg = redact.RedactedMarker()
				r.redactable = true
			}
			return r
		}
	case WithoutSensitiveDataNorMarkers:
		return func(r redactablePackage) redactablePackage {
			if r.redactable {
				r.msg = redact.RedactableBytes(r.msg).Redact().StripMarkers()
				r.redactable = false
			} else {
				r.msg = strippedMarker
			}
			return r
		}
	default:
		panic(errors.AssertionFailedf("unrecognized mode: %v", editMode))
	}
}

var strippedMarker = redact.RedactableBytes(redact.RedactedMarker()).StripMarkers()

// maybeRedactEntry transforms a logpb.Entry to either strip
// sensitive data or keep it, or strip the redaction markers or keep them,
// or a combination of both. The specific behavior is selected
// by the provided redactEditor.
func maybeRedactEntry(entry logpb.Entry, editor redactEditor) logpb.Entry {
	r := redactablePackage{
		redactable: entry.Redactable,
		msg:        []byte(entry.Message),
	}
	r = editor(r)
	entry.Message = string(r.msg)
	entry.Redactable = r.redactable

	r = redactablePackage{
		redactable: entry.Redactable,
		msg:        []byte(entry.Tags),
	}
	r = editor(r)
	entry.Tags = string(r.msg)
	return entry
}

// Safe constructs a SafeFormatter / SafeMessager.
// This is obsolete. Use redact.Safe directly.
// TODO(knz): Remove this.
var Safe = redact.Safe

func init() {
	// We consider booleans and numeric values to be always safe for
	// reporting. A log call can opt out by using redact.Unsafe() around
	// a value that would be otherwise considered safe.
	redact.RegisterSafeType(reflect.TypeOf(true)) // bool
	redact.RegisterSafeType(reflect.TypeOf(123))  // int
	redact.RegisterSafeType(reflect.TypeOf(int8(0)))
	redact.RegisterSafeType(reflect.TypeOf(int16(0)))
	redact.RegisterSafeType(reflect.TypeOf(int32(0)))
	redact.RegisterSafeType(reflect.TypeOf(int64(0)))
	redact.RegisterSafeType(reflect.TypeOf(uint8(0)))
	redact.RegisterSafeType(reflect.TypeOf(uint16(0)))
	redact.RegisterSafeType(reflect.TypeOf(uint32(0)))
	redact.RegisterSafeType(reflect.TypeOf(uint64(0)))
	redact.RegisterSafeType(reflect.TypeOf(float32(0)))
	redact.RegisterSafeType(reflect.TypeOf(float64(0)))
	redact.RegisterSafeType(reflect.TypeOf(complex64(0)))
	redact.RegisterSafeType(reflect.TypeOf(complex128(0)))
	// Signal names are also safe for reporting.
	redact.RegisterSafeType(reflect.TypeOf(os.Interrupt))
	// Times and durations too.
	redact.RegisterSafeType(reflect.TypeOf(time.Time{}))
	redact.RegisterSafeType(reflect.TypeOf(time.Duration(0)))
	// Encoded types should always be safe to report.
	redact.RegisterSafeType(reflect.TypeOf(encodingtype.T(0)))
	// Channel names are safe to report.
	redact.RegisterSafeType(reflect.TypeOf(Channel(0)))
}

type redactablePackage struct {
	msg        []byte
	redactable bool
}

const redactableIndicator = "⋮"

var redactableIndicatorBytes = []byte(redactableIndicator)

func renderTagsAsRedactable(ctx context.Context, buf *strings.Builder) {
	tags := logtags.FromContext(ctx)
	if tags == nil {
		return
	}
	comma := ""
	for _, t := range tags.Get() {
		buf.WriteString(comma)
		buf.WriteString(t.Key())
		if v := t.Value(); v != nil && v != "" {
			if len(t.Key()) > 1 {
				buf.WriteByte('=')
			}
			redact.Fprint(buf, v)
		}
		comma = ","
	}
}

// TestingSetRedactable sets the redactable flag on the file output of
// the debug logger for usage in a test. The caller is responsible
// for calling the cleanup function. This is exported for use in
// tests only -- it causes the logging configuration to be at risk of
// leaking unsafe information due to asynchronous direct writes to fd
// 2 / os.Stderr.
//
// See the discussion on SetupRedactionAndStderrRedirects() for
// details.
//
// This is not safe for concurrent use with logging operations.
func TestingSetRedactable(redactableLogs bool) (cleanup func()) {
	prevEditors := make([]redactEditor, len(debugLog.sinkInfos))
	for i := range debugLog.sinkInfos {
		prevEditors[i] = debugLog.sinkInfos[i].editor
		debugLog.sinkInfos[i].editor = getEditor(SelectEditMode(false /* redact */, redactableLogs))
	}
	return func() {
		for i, e := range prevEditors {
			debugLog.sinkInfos[i].editor = e
		}
	}
}
