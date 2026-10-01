package system

import (
	"context"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/live/internal"
	livemock "github.com/bililive-go/bililive-go/src/live/mock"
)

// 初始化完成后会用 OriginalLive 顶掉包装对象，期间热应用的房间选项必须同时落到原始 Live，
// 否则表现为"改了设置/重新扫码，老房间仍按改前的状态取流"。
func TestInitializingLiveForwardsOptionsToOriginal(t *testing.T) {
	ctrl := gomock.NewController(t)
	original := livemock.NewMockLive(ctrl)
	u, err := url.Parse("https://www.douyu.com/123456")
	assert.NoError(t, err)
	room := &configs.LiveRoom{Url: "https://www.douyu.com/123456", AudioOnly: true, NickName: "主播"}
	original.EXPECT().UpdateLiveOptionsbyConfig(gomock.Any(), room).Return(nil)

	l := &InitializingLive{BaseLive: internal.NewBaseLive(u), OriginalLive: original}
	assert.NoError(t, l.UpdateLiveOptionsbyConfig(context.Background(), room))

	opts := l.GetOptions()
	if assert.NotNil(t, opts) {
		assert.True(t, opts.AudioOnly, "包装对象自身也要更新：初始化期间读的是它")
		assert.Equal(t, "主播", opts.NickName)
	}
}

// 原始 Live 拒绝时错误必须原样返回，交给上层记日志，而不是静默只更新一半
func TestInitializingLivePropagatesOriginalUpdateError(t *testing.T) {
	ctrl := gomock.NewController(t)
	original := livemock.NewMockLive(ctrl)
	u, err := url.Parse("https://www.douyu.com/123456")
	assert.NoError(t, err)
	room := &configs.LiveRoom{Url: "https://www.douyu.com/123456"}
	original.EXPECT().UpdateLiveOptionsbyConfig(gomock.Any(), room).Return(context.DeadlineExceeded)

	l := &InitializingLive{BaseLive: internal.NewBaseLive(u), OriginalLive: original}
	assert.ErrorIs(t, l.UpdateLiveOptionsbyConfig(context.Background(), room), context.DeadlineExceeded)
}
