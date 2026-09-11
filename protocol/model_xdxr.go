package protocol

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"time"
)

// 除权除息/股本变迁(0x000f)
//
// 线路格式为新协议头,与 K 线等旧命令不同,经实测:
//
//	请求: 0c + MsgID(4B,服务端原样回显) + 01 + 0b00 + 0b00 + 0f00 + 0100 + market(1B) + code(6B)
//	响应: 跳过9字节(2B未知+market+code回显) + num(u16) + num × 记录
//	记录: market(1B) + code(6B) + pad(1B) + date(u32 YYYYMMDD) + category(1B) + chunk(16B)
//	category==1 时 chunk 为 <ffff: 分红/配股价/送转股/配股,均为每10股口径
//
// 注意: Frame() 不分配 MsgID,调用方必须写入唯一 MsgID(服务端回显该值用于异步匹配),
// 原因是 SendFrame 会覆盖 MsgID,所以 GetXdxrInfo 走了独立发送路径,见 client.go。
// 参考: rainx/pytdx parser/get_xdxr_info.py, easy_tdx commands/xdxr_info.py

// XdxrCategoryNames 除权类别映射(来自 pytdx)
var XdxrCategoryNames = map[uint8]string{
	1:  "除权除息",
	2:  "送配股上市",
	3:  "非流通股上市",
	4:  "未知股本变动",
	5:  "股本变化",
	6:  "增发新股",
	7:  "股份回购",
	8:  "增发新股上市",
	9:  "转配股上市",
	10: "可转债上市",
	11: "扩缩股",
	12: "非流通股缩股",
	13: "送认购权证",
	14: "送认沽权证",
}

// XdxrRecord 单条股本变迁记录
type XdxrRecord struct {
	Date         time.Time `json:"-"`        //事件日期(15:00,同日K线口径)
	DateStr      string    `json:"date"`     //事件日期 YYYY-MM-DD
	Category     uint8     `json:"category"` //类别编号
	CategoryName string    `json:"category_name"`
	// 以下仅 category==1(除权除息)有效,均为每10股口径
	Fenhong     float64 `json:"fenhong"`     //分红金额
	Peigujia    float64 `json:"peigujia"`    //配股价
	Songzhuangu float64 `json:"songzhuangu"` //送转股数
	Peigu       float64 `json:"peigu"`       //配股数
	Raw         string  `json:"raw"`         //16字节原文(hex,供排查)
}

// XdxrResp 除权除息响应
type XdxrResp struct {
	Code    string        `json:"code"` //6位代码(无前缀),由调用方回填
	Records []*XdxrRecord `json:"records"`
}

type xdxr struct{}

// Frame 组装新协议头请求包,MsgID 由调用方赋值(必须唯一,服务端回显)
func (xdxr) Frame(exchange Exchange, code string) *Frame {
	data := make([]byte, 0, 9)
	data = append(data, 0x01, 0x00) //新协议固定前缀
	data = append(data, exchange.Uint8())
	data = append(data, []byte(code)...)
	return &Frame{
		Control: Control01,
		Type:    TypeXdxr,
		Data:    data,
	}
}

// Decode 解析响应 Data 域
func (xdxr) Decode(bs []byte) (*XdxrResp, error) {
	if len(bs) < 11 {
		return nil, errors.New("除权数据长度不足")
	}
	pos := 9 //跳过2字节未知+market+code回显
	num := int(Uint16(bs[pos : pos+2]))
	pos += 2

	resp := &XdxrResp{Records: make([]*XdxrRecord, 0, num)}
	for i := 0; i < num; i++ {
		if len(bs[pos:]) < 29 {
			return nil, fmt.Errorf("除权数据长度不足:第%d条", i)
		}
		pos += 7 //market(1B)+code(6B),与请求一致,无需校验
		pos++    //跳过1字节pad

		zipday := Uint32(bs[pos : pos+4])
		pos += 4
		year := int(zipday / 10000)
		month := time.Month((zipday % 10000) / 100)
		day := int(zipday % 100)
		if month < 1 || month > 12 || day < 1 || day > 31 {
			return nil, fmt.Errorf("除权日期非法:第%d条 raw=%d", i, zipday)
		}

		category := bs[pos]
		pos++

		chunk := bs[pos : pos+16]
		pos += 16

		r := &XdxrRecord{
			Date:         time.Date(year, month, day, 15, 0, 0, 0, time.Local),
			Category:     category,
			CategoryName: XdxrCategoryNames[category],
			Raw:          hex.EncodeToString(chunk),
		}
		if r.CategoryName == "" {
			r.CategoryName = fmt.Sprintf("未知(%d)", category)
		}
		r.DateStr = r.Date.Format("2006-01-02")
		if category == 1 {
			r.Fenhong = float64(math.Float32frombits(Uint32(chunk[0:4])))
			r.Peigujia = float64(math.Float32frombits(Uint32(chunk[4:8])))
			r.Songzhuangu = float64(math.Float32frombits(Uint32(chunk[8:12])))
			r.Peigu = float64(math.Float32frombits(Uint32(chunk[12:16])))
		}
		resp.Records = append(resp.Records, r)
	}
	return resp, nil
}
