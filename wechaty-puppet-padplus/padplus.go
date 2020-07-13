package wechaty_puppet_padplus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"google.golang.org/grpc"

	wechatyPuppet "github.com/wechaty/go-wechaty/wechaty-puppet"
	"github.com/wechaty/go-wechaty/wechaty-puppet-padplus/cache"
	"github.com/wechaty/go-wechaty/wechaty-puppet-padplus/payload"
	pd "github.com/wechaty/go-wechaty/wechaty-puppet-padplus/proto"
	"github.com/wechaty/go-wechaty/wechaty-puppet/schemas"
)

// PuppetPadPlus struct
type PuppetPadPlus struct {
	*wechatyPuppet.Puppet

	option      *wechatyPuppet.Option
	grpcConn    *grpc.ClientConn
	grpcClient  pd.PadPlusServerClient
	eventStream pd.PadPlusServer_InitClient
	Uin         string

	messagePayload *cache.MessagePayload
	contactPayload *cache.ContactPayload

	selfContact *payload.ContactPayload
}

// NewPuppetPadPlus new PuppetHostie struct
func NewPuppetPadPlus(o *wechatyPuppet.Option) (*PuppetPadPlus, error) {
	if o.Token == "" {
		o.Token = WechatyPuppetToken
	}
	if o.Endpoint == "" {
		o.Endpoint = WechatyPuppetEndpoint
	}

	puppetAbstract, err := wechatyPuppet.NewPuppet(*o)
	if err != nil {
		return nil, err
	}
	puppetPadPlus := &PuppetPadPlus{
		Puppet:         puppetAbstract,
		messagePayload: cache.NewMessagePayload(),
		contactPayload: cache.NewContactPayload(),
	}
	puppetAbstract.SetPuppetImplementation(puppetPadPlus)
	return puppetPadPlus, nil
}

// Start ...
func (p *PuppetPadPlus) Start() (err error) {
	log.Println("PuppetPadPlus Start()")
	defer func() {
		if err != nil {
			err = fmt.Errorf("PuppetHostie Start() rejection: %w", err)
		}
	}()

	err = p.startGrpcClient()
	if err != nil {
		return err
	}
	err = p.startGrpcStream()
	if err != nil {
		return err
	}

	if p.isLogin() {
		err = p.AutoLogin()
		if err != nil {
			return err
		}
	} else {
		err = p.Login()
		if err != nil {
			return err
		}
	}

	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()

		for {
			<-t.C
			_, _ = p.Request(pd.ApiType_HEARTBEAT, "")
		}
	}()

	return nil
}

// Stop ...
func (p *PuppetPadPlus) Stop() {
	var err error
	defer func() {
		if err != nil {
			log.Printf("PuppetHostie Stop err: %s\n", err)
		}
	}()
	if p.isLogin() {
		p.Emit(schemas.EventLogoutPayload{
			ContactId: p.Uin,
			Data:      "PuppetPadPlus Stop()",
		})
		p.Uin = ""
	}

	if err = p.stopGrpcStream(); err != nil {
		return
	}

	if err = p.stopGrpcClient(); err != nil {
		return
	}
}

// startGrpcClient start GRPC Client
func (p *PuppetPadPlus) startGrpcClient() error {
	endpoint := Endpoint
	if len(p.Endpoint) > 0 {
		endpoint = p.Endpoint
	}
	conn, err := grpc.Dial(endpoint, grpc.WithInsecure())
	if err != nil {
		return err
	}
	p.grpcConn = conn
	p.grpcClient = pd.NewPadPlusServerClient(conn)
	return nil
}

// startGrpcStream start GRPC Stream
func (p *PuppetPadPlus) startGrpcStream() (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("startGrpcStream err:%w", err)
		}
	}()
	if p.eventStream != nil {
		return errors.New("event stream exists")
	}
	p.eventStream, err = p.grpcClient.Init(context.Background(), &pd.InitConfig{
		Token: &p.Token,
	})
	if err != nil {
		return err
	}

	go func() {
		for {
			reply, err := p.eventStream.Recv()
			if err == io.EOF {
				log.Println("eventStream.Recv EOF")
				break
			}
			if err != nil {
				log.Printf("PuppetHostie startGrpcStream() eventStream err %s", err)
				reason := "startGrpcStream() eventStream err: " + err.Error()
				p.Emit(schemas.PuppetEventNameReset, schemas.EventResetPayload{Data: reason})
				break
			}
			go p.onGrpcStreamEvent(reply)
		}
	}()
	return nil
}

func (p *PuppetPadPlus) stopGrpcClient() error {
	if p.grpcConn == nil {
		return errors.New("puppetClient had not inited")
	}
	p.grpcConn.Close()
	p.grpcConn = nil
	p.grpcClient = nil
	return nil
}

// stopGrpcStream stop GRPC Stream
func (p *PuppetPadPlus) stopGrpcStream() error {
	log.Println("PuppetPadPlus stopGrpcStream()")

	if p.eventStream == nil {
		return errors.New("no event stream")
	}

	if err := p.eventStream.CloseSend(); err != nil {
		log.Printf("PuppetHostie stopGrpcStream() err: %s\n", err)
	}
	p.eventStream = nil
	return nil
}

// isLogin is login
func (p *PuppetPadPlus) isLogin() bool {
	return p.Uin != ""
}

var pbEventType2PuppetEventName = map[pd.ResponseType]schemas.PuppetEventName{
	pd.ResponseType_LOGIN_QRCODE:   schemas.PuppetEventNameScan,   // scan qrcode
	pd.ResponseType_QRCODE_SCAN:    schemas.PuppetEventNameScan,   // scan qrcode
	pd.ResponseType_ACCOUNT_LOGOUT: schemas.PuppetEventNameLogout, // logout

	pd.ResponseType_MESSAGE_RECEIVE: schemas.PuppetEventNameMessage, // message

	pd.ResponseType_QRCODE_LOGIN:  schemas.PuppetEventNameLogin,
	pd.ResponseType_AUTO_LOGIN:    schemas.PuppetEventNameLogin,
	pd.ResponseType_ACCOUNT_LOGIN: schemas.PuppetEventNameLogin,
}

var pbEventType2GeneratePayloadFunc = map[pd.ResponseType]func() interface{}{
	pd.ResponseType_QRCODE_LOGIN: func() interface{} { return &payload.QrCodeLogin{} },

	pd.ResponseType_MESSAGE_RECEIVE: func() interface{} { return &payload.MessagePayload{} },
	pd.ResponseType_CONTACT_LIST:    func() interface{} { return &payload.ContactPayload{} },
	pd.ResponseType_CONTACT_MODIFY:  func() interface{} { return &payload.RPCContactPayload{} },

	pd.ResponseType_LOGIN_QRCODE:   func() interface{} { return &payload.EventPadPlusQrCode{} }, // login qr
	pd.ResponseType_QRCODE_SCAN:    func() interface{} { return &payload.EventScanData{} },
	pd.ResponseType_ACCOUNT_LOGOUT: func() interface{} { return &payload.LogoutGRPCResponse{} }, // logout
	pd.ResponseType_AUTO_LOGIN:     func() interface{} { return &payload.AutoLoginResponse{} },  // logout
}

// eventPayload2PuppetPayload grpc payload to puppet payload
func (p *PuppetPadPlus) eventPayload2PuppetPayload(data interface{}) interface{} {
	switch t := data.(type) {
	default:
		fmt.Printf("unexpected type %T", t) // %T prints whatever type t has
	case *payload.EventScanData:
		return &schemas.EventScanPayload{
			Status: data.(*payload.EventScanData).Status.ToPuppetStatus(),
			QrCode: data.(*payload.EventScanData).QrCodeId,
		}
	case *payload.EventPadPlusQrCode:
		return &schemas.EventScanPayload{
			BaseEventPayload: schemas.BaseEventPayload{Data: data.(*payload.EventPadPlusQrCode).QrCode},
			Status:           0,
			QrCode:           data.(*payload.EventPadPlusQrCode).QrCodeId,
		}
	case *payload.LogoutGRPCResponse:
		js, _ := json.Marshal(data)
		return &schemas.EventLogoutPayload{
			ContactId: data.(*payload.LogoutGRPCResponse).Uin,
			Data:      string(js),
		}
	case *payload.MessagePayload:
		p.messagePayload.Store(data.(*payload.MessagePayload).MsgId, *data.(*payload.MessagePayload))
		return &schemas.EventMessagePayload{
			MessageId: data.(*payload.MessagePayload).MsgId,
		}
	case *payload.QrCodeLogin: // login
		p.SetID(data.(*payload.QrCodeLogin).UserName)
		return &schemas.EventLoginPayload{
			ContactId: data.(*payload.QrCodeLogin).UserName,
		}
	case *payload.AutoLoginResponse:
		p.SetID(data.(*payload.AutoLoginResponse).WechatUser.UserName)
		return &schemas.EventLoginPayload{
			ContactId: data.(*payload.AutoLoginResponse).WechatUser.UserName,
		}
	}
	return data
}

// onGrpcStreamEvent grpc stream handle
// Another instance connected, disconnected the current one.
// EXPIRED_TOKEN
// INVALID_TOKEN
func (p *PuppetPadPlus) onGrpcStreamEvent(resp *pd.StreamResponse) {
	log.Printf("PuppetPadPlus onGrpcStreamEvent({type:%s payload:%+v})", *resp.ResponseType, *resp.Data)
	if *resp.ResponseType != pd.ResponseType_CONTACT_LIST {
		log.Printf("Meessage: traceID: %s, requestID: %s, Uin: %s", resp.GetTraceId(), resp.GetRequestId(), resp.GetUin())
	}

	if *resp.Data == "EXPIRED_TOKEN" || *resp.Data == "INVALID_TOKEN" {
		log.Printf("'token error: %s !\n", *resp.Data)
		return
	}

	eventName, ok := pbEventType2PuppetEventName[*resp.ResponseType]
	if !ok && *resp.ResponseType != pd.ResponseType_CONTACT_LIST {
		log.Printf("'eventType %s unsupported! (code should not reach here)\n", *resp.ResponseType)
		return
	}

	// unmarshal
	data := pbEventType2GeneratePayloadFunc[*resp.ResponseType]()
	p.unMarshal(*resp.Data, data)

	// 内部事件
	switch *resp.ResponseType {
	case pd.ResponseType_QRCODE_LOGIN: // ok 登录成功事件
		// p.SetID(data.(*payload.QrCodeLogin).UserName)
		p.contactPayload.Store(data.(*payload.QrCodeLogin).UserName, payload.ContactPayload{
			Alias:       data.(*payload.QrCodeLogin).Alias,
			ContactType: 3,
			BigHeadUrl:  data.(*payload.QrCodeLogin).HeadImgUrl,
			NickName:    data.(*payload.QrCodeLogin).NickName,
			Sex:         payload.ContactGenderUnknown,
			UserName:    data.(*payload.QrCodeLogin).UserName,
		})
	case pd.ResponseType_ACCOUNT_LOGOUT:
		p.SetID("")
	case pd.ResponseType_MESSAGE_RECEIVE: // 接收到消息
	case pd.ResponseType_CONTACT_LIST: // 联系人
		p.contactPayload.Store(data.(*payload.ContactPayload).UserName, *data.(*payload.ContactPayload))
	case pd.ResponseType_CONTACT_MODIFY:
		p.contactPayload.Store(data.(*payload.RPCContactPayload).UserName, *data.(*payload.RPCContactPayload).ToContactPayload())
	}

	p.Emit(eventName, p.eventPayload2PuppetPayload(data))
	return
}

// unMarshal unmarshal json
func (p *PuppetPadPlus) unMarshal(data string, v interface{}) {
	err := json.Unmarshal([]byte(data), v)
	if err != nil {
		log.Printf("PuppetPadPlus unMarshal err: %s, data: %s\n", err, data)
	}
}

// onMessage on message handle
func (p *PuppetPadPlus) onMessage(data string) (err error) {
	var pay payload.MessagePayload
	err = json.Unmarshal([]byte(data), &pay)
	if err != nil {
		return
	}
	switch pay.MsgType {
	case payload.WechatMessageTypeImage:
		log.Println("获取图片媒资信息")
	}
	return
}

// isRoomId is room
func isRoomId(s string) bool {
	return strings.HasSuffix(s, "@chatroom")
}

// loadRichMediaData load media data
func (p *PuppetPadPlus) loadRichMediaData(data payload.PadPlusRichMediaData) (payload.PadPlusMediaData, error) {
	res, err := p.Request(pd.ApiType_GET_MESSAGE_MEDIA, data)
	if err != nil {
		return payload.PadPlusMediaData{}, fmt.Errorf("PuppetPadPlus MessageSendText err: %w", err)
	}
	var pay payload.PadPlusMediaData
	p.unMarshal(res, &pay)
	return pay, nil
}