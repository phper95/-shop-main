package order_service

import (
	"encoding/json"
	"errors"
	"gitee.com/phper95/pkg/cache"
	"gitee.com/phper95/pkg/mq"
	"github.com/Shopify/sarama"
	"github.com/gin-gonic/gin"
	"github.com/jinzhu/copier"
	"github.com/segmentio/ksuid"
	"github.com/shopspring/decimal"
	"github.com/unknwon/com"
	"gorm.io/gorm"
	"shop/internal/models"
	"shop/internal/models/vo"
	"shop/internal/params"
	"shop/internal/service/cart_service"
	cartVo "shop/internal/service/cart_service/vo"
	orderDto "shop/internal/service/order_service/dto"
	ordervo "shop/internal/service/order_service/vo"
	userVO "shop/internal/service/wechat_user_service/vo"
	"shop/pkg/constant"
	orderEnum "shop/pkg/enums/order"
	"shop/pkg/global"
	"shop/pkg/util"
	"strconv"
	"strings"
	"time"
)

type Order struct {
	Id   int64
	Name string

	Enabled int

	PageNum  int
	PageSize int

	M *models.StoreOrder

	Ids []int64

	Uid int64

	CartId string

	Type string

	User *models.ShopUser

	ComputeParam *params.ComputeOrderParam
	Key          string
	OrderParam   *params.OrderParam
	OrderId      string
	IntType      int

	ReplyParam []params.ProductReplyParam
}

//订单列表 -1全部 默认为0未支付 1待发货 2待收货 3待评价 4已完成
func (d *Order) GetList() ([]ordervo.StoreOrder, int, int) {
	maps := make(map[string]interface{})
	maps["uid"] = d.Uid

	switch d.IntType {
	case -1:
	case 0:
		maps["paid"] = 0
		maps["refund_status"] = 0
		maps["status"] = 0

	case 1:
		maps["paid"] = 1
		maps["refund_status"] = 0
		maps["status"] = 0
	case 2:
		maps["paid"] = 1
		maps["refund_status"] = 0
		maps["status"] = 1
	case 3:
		maps["paid"] = 1
		maps["refund_status"] = 0
		maps["status"] = 2
	case 4:
		maps["paid"] = 1
		maps["refund_status"] = 0
		maps["status"] = 3
	case 5:
		maps["paid"] = 1
		maps["refund_status"] = 1
	case 6:
		maps["paid"] = 0
		maps["refund_status"] = 2
	}

	var ListVo []ordervo.StoreOrder
	var ReturnListVo []ordervo.StoreOrder

	total, list := models.GetAllOrder(d.PageNum, d.PageSize, maps)
	e := copier.Copy(&ListVo, list)
	if e != nil {
		global.LOG.Error(e)
	}
	totalNum := util.Int64ToInt(total)
	totalPage := util.GetTotalPage(totalNum, d.PageSize)
	for _, val := range ListVo {
		vo := HandleOrder(&val)
		ReturnListVo = append(ReturnListVo, *vo)
	}
	return ReturnListVo, totalNum, totalPage
}

//取消订单
func (d *Order) CancelOrder() (*models.StoreOrder, error) {
	var err error
	tx := global.Db.Begin()
	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()
	order, mOrder, err := d.GetOrderInfo()
	if err != nil {
		return mOrder, errors.New("订单不存在")
	}
	if order.Paid == 1 {
		return mOrder, errors.New("支付订单不可以取消")
	}
	err = RegressionStock(tx, order)
	if err != nil {
		global.LOG.Error(err)
		return mOrder, errors.New("取消失败-001")
	}

	//删除订单
	err = tx.Where("id = ?", order.Id).Delete(&models.StoreOrder{}).Error
	if err != nil {
		global.LOG.Error(err)
		return mOrder, errors.New("取消失败-002")
	}

	return mOrder, nil

}

//回退库存
func RegressionStock(tx *gorm.DB, order *ordervo.StoreOrder) error {
	var err error
	orderInfo := HandleOrder(order)
	cartInfoList := orderInfo.CartInfo
	for _, vo := range cartInfoList {
		err = tx.Exec("update store_product_attr_value set stock=stock + ?, sales=sales - ?"+
			" where product_id = ? and `unique` = ? and stock >= ?",
			vo.CartNum, vo.CartNum, vo.ProductId, vo.ProductAttrUnique, vo.CartNum).Error
		if err != nil {
			return err
		}
		err = tx.Exec("update store_product set stock=stock + ?, sales=sales - ?"+
			" where id = ? and stock >= ?",
			vo.CartNum, vo.CartNum, vo.ProductId, vo.CartNum).Error
		if err != nil {
			return err
		}
	}

	return nil
}

//todo 回退积分
//todo 回退优惠券

//订单评价
func (d *Order) OrderComment() (*models.StoreOrder, error) {
	var err error
	tx := global.Db.Begin()
	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()
	order, mOrder, err := d.GetOrderInfo()
	if err != nil {
		return mOrder, errors.New("订单不存在")
	}
	var replys []models.StoreProductReply
	for _, data := range d.ReplyParam {
		reply := models.StoreProductReply{
			Uid:          d.Uid,
			Oid:          order.Id,
			ProductId:    data.ProductId,
			ProductScore: data.ProductScore,
			ServiceScore: data.ServiceScore,
			Comment:      data.Comment,
			Pics:         data.Pics,
			Unique:       data.Unique,
		}
		replys = append(replys, reply)
	}
	err = tx.Model(&models.StoreProductReply{}).Select("uid", "oid", "product_id",
		"product_score", "service_score", "comment", "pics", "unique").Create(replys).Error
	if err != nil {
		global.LOG.Error(err)
		return mOrder, errors.New("评价失败-0001")
	}
	err = tx.Model(&models.StoreOrder{}).Where("id = ?", order.Id).Update("status", 3).Error
	if err != nil {
		global.LOG.Error(err)
		return mOrder, errors.New("评价失败-0002")
	}
	mOrder.Status = 3
	return mOrder, nil
}

//收货
func (d *Order) TakeOrder() (*models.StoreOrder, error) {
	var err error
	tx := global.Db.Begin()
	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()
	order, mOrder, err := d.GetOrderInfo()
	if err != nil {
		return mOrder, errors.New("订单不存在")
	}
	if order.Status != 1 {
		return mOrder, errors.New("订单状态错误")
	}

	//修改订单状态
	err = tx.Model(&models.StoreOrder{}).Where("id = ?", order.Id).Update("status", 2).Error
	if err == nil {
		mOrder.Status = 2
		//增加状态
		err = models.AddStoreOrderStatus(tx, order.Id, "user_take_delivery", "用户已收货")

		//奖励积分
		if order.GainIntegral > 0 {
			err = tx.Exec("update user set integral = integral + ?"+
				" where id = ?", order.Uid, order.GainIntegral).Error
			if err != nil {
				global.LOG.Error(err)
				return mOrder, err
			}
			//增加流水
			number, _ := com.StrTo(order.GainIntegral).Float64()
			mark := "购买商品赠送积分" + com.ToStr(order.GainIntegral) + "积分"
			err = models.Income(tx, order.Uid, "购买商品赠送积分", "integral", "gain",
				mark, com.ToStr(order.Id), number, number)
			if err != nil {
				global.LOG.Error(err)
				return mOrder, err
			}
		}
	}

	//todo 分销

	return mOrder, nil
}

//创建订单
func (d *Order) CreateOrder() (*models.StoreOrder, error) {
	var err error
	tx := global.Db.Begin()
	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()
	d.ComputeParam = &params.ComputeOrderParam{
		AddressId:    d.OrderParam.AddressId,
		CouponId:     d.OrderParam.CouponId,
		UseIntegral:  d.OrderParam.UseIntegral,
		ShippingType: d.OrderParam.ShippingType,
	}
	computeVo, err := d.ComputeOrder()
	if err != nil {
		return nil, err
	}

	//todo 门店

	var (
		userAddress models.UserAddress
		totalNum    = 0
		cartIds     = make([]string, 0)
		//integral = 0
		gainIntegral = 0
	)
	err = global.Db.Model(&models.UserAddress{}).
		Where("uid = ?", d.Uid).
		Where("id = ?", d.OrderParam.AddressId).
		First(&userAddress).Error
	if err != nil {
		return nil, errors.New("地址出错")
	}

	cacheDto, _ := getCacheOrderInfo(d.Uid, d.Key)
	cartInfo := cacheDto.CartInfo
	for _, cart := range cartInfo {
		err = cart_service.CheckStock(cart.ProductId, cart.CartNum, cart.ProductAttrUnique)
		if err != nil {
			return nil, err
		}
		cartIds = append(cartIds, strconv.FormatInt(cart.Id, 10))
		totalNum = totalNum + cart.CartNum

		//积分
		gain := cart.ProductInfo.GiveIntegral
		if gain > 0 {
			gainIntegral = gainIntegral + cart.CartNum*gain
		}

	}
	worker, _ := util.NewWorker(int64(1))
	orderSn := worker.GetId()
	detailAddr := userAddress.Province + " " + userAddress.City + " " + userAddress.District + " " + userAddress.Detail
	storeOrder := &models.StoreOrder{
		Uid:            d.Uid,
		OrderId:        strconv.FormatInt(orderSn, 10),
		RealName:       userAddress.RealName,
		UserPhone:      userAddress.Phone,
		UserAddress:    detailAddr,
		CartId:         strings.Join(cartIds, ","),
		TotalNum:       totalNum,
		TotalPrice:     computeVo.TotalPrice,
		TotalPostage:   computeVo.PayPostage,
		CouponId:       d.OrderParam.CouponId,
		CouponPrice:    computeVo.CouponPrice,
		PayPrice:       computeVo.PayPrice,
		PayPostage:     computeVo.PayPostage,
		DeductionPrice: computeVo.DeductionPrice,
		Paid:           orderEnum.PAY_STATUS_0,
		UseIntegral:    computeVo.UseIntegral,
		BackIntegral:   0,
		GainIntegral:   gainIntegral,
		Mark:           d.OrderParam.Mark,
		Cost:           cacheDto.PriceGroup.CostPrice,
		Unique:         d.Key,
		ShippingType:   d.OrderParam.ShippingType,
		PayType:        d.OrderParam.PayType,
	}
	err = tx.Model(&models.StoreOrder{}).
		Select("uid", "order_id", "real_name", "user_phone", "user_address", "cart_id", "total_num",
			"total_price", "total_postage", "coupon_id", "coupon_price", "pay_price", "pay_postage", "deduction_price",
			"paid", "use_integral", "back_integral", "gain_integral", "mark", "unique", "shipping_type", "pay_type").
		Create(storeOrder).Error
	if err != nil {
		return nil, errors.New("订单生成失败")
	}
	//todo 扣积分
	//todo 消费优惠券
	orderInfoList := make([]models.StoreOrderCartInfo, 0)
	//减库存加销量
	for _, vo := range cartInfo {
		err = tx.Exec("update store_product_attr_value set stock=stock - ?, sales=sales + ?"+
			" where product_id = ? and `unique` = ? and stock >= ?",
			vo.CartNum, vo.CartNum, vo.ProductId, vo.ProductAttrUnique, vo.CartNum).Error
		if err != nil {
			return nil, errors.New("库存扣减失败-00000")
		}
		err = tx.Exec("update store_product set stock=stock - ?, sales=sales + ?"+
			" where id = ? and stock >= ?",
			vo.CartNum, vo.CartNum, vo.ProductId, vo.CartNum).Error
		if err != nil {
			return nil, errors.New("库存扣减失败-00001")
		}

		b, _ := json.Marshal(vo)
		uuid := ksuid.New()
		orderCartInfo := models.StoreOrderCartInfo{
			Oid:          storeOrder.Id,
			OrderId:      storeOrder.OrderId,
			CartId:       vo.Id,
			ProductId:    vo.ProductId,
			CartInfo:     string(b),
			Unique:       uuid.String(),
			IsAfterSales: 1,
		}
		orderInfoList = append(orderInfoList, orderCartInfo)
	}

	//保存购物车商品信息
	err = tx.Model(&models.StoreOrderCartInfo{}).Create(orderInfoList).Error
	if err != nil {
		return nil, errors.New("订单创建失败-00001")
	}

	//增加状态
	err = models.AddStoreOrderStatus(tx, storeOrder.Id, "create_order", "订单生成")
	if err != nil {
		return nil, errors.New("订单创建失败-00002")
	}

	//todo 订单自动取消处理

	return storeOrder, nil
}

func (d *Order) GetOrderInfo() (*ordervo.StoreOrder, *models.StoreOrder, error) {
	var (
		order *models.StoreOrder
		vo    ordervo.StoreOrder
	)

	maps := make(map[string]interface{})
	maps["order_id"] = d.OrderId
	if d.Uid > 0 {
		maps["uid"] = d.Uid
	}
	err := global.Db.Model(&models.StoreOrder{}).
		Where(maps).First(&order).Error
	if err != nil {
		global.LOG.Error(err)
		return nil, nil, err
	}
	copier.Copy(&vo, order)

	return &vo, order, nil
}

//处理订单状态
func HandleOrder(order *ordervo.StoreOrder) *ordervo.StoreOrder {
	var (
		orderInfoList []models.StoreOrderCartInfo
		cart          cartVo.Cart
		statusDto     orderDto.Status
	)
	global.Db.Model(&models.StoreOrderCartInfo{}).Where("oid = ?", order.Id).Find(&orderInfoList)
	cartInfo := make([]cartVo.Cart, 0)
	for _, orderInfo := range orderInfoList {
		json.Unmarshal([]byte(orderInfo.CartInfo), &cart)
		cart.Unique = orderInfo.Unique
		cartInfo = append(cartInfo, cart)
	}

	order.CartInfo = cartInfo

	if order.Paid == 0 {
		statusDto.Class = "nobuy"
		statusDto.Msg = "未支付"
		statusDto.Type = "0"
		statusDto.Title = "未支付"
	} else if order.RefundStatus == 1 {
		statusDto.Class = "state-sqtk"
		statusDto.Msg = "商家审核中，请耐心等待"
		statusDto.Type = "-1"
		statusDto.Title = "申请退款中"
	} else if order.RefundStatus == 2 {
		statusDto.Class = "state-sqtk"
		statusDto.Msg = "已经为你退款，感谢您的支付"
		statusDto.Type = "-2"
		statusDto.Title = "已退款"
	} else if order.Status == 0 {
		if order.ShippingType == 1 {
			statusDto.Class = "state-nfh"
			statusDto.Msg = "商家未发货，请耐心等待"
			statusDto.Type = "1"
			statusDto.Title = "未发货"
		}
	} else if order.Status == 2 {
		statusDto.Class = "state-ypj"
		statusDto.Msg = "已收货，快去评价一下吧"
		statusDto.Type = "3"
		statusDto.Title = "待评价"
	} else if order.RefundStatus == 3 {
		statusDto.Class = "state-ytk"
		statusDto.Msg = "交易完成，感谢您的支持"
		statusDto.Type = "4"
		statusDto.Title = "交易完成"
	}

	if order.PayType == "weixin" {
		statusDto.PayType = "微信支付"
	} else {
		statusDto.PayType = "余额支付"
	}
	order.StatusDto = statusDto

	return order
}

func (d *Order) Check() (map[string]interface{}, error) {
	if d.Key == "" {
		return nil, errors.New("参数错误")
	}
	var order *models.StoreOrder
	error := global.Db.Model(&models.StoreOrder{}).
		Where("`unique` = ?", d.Key).
		Where("uid = ?", d.Uid).First(&order).Error
	if error == nil {
		orderExtendDto := &orderDto.OrderExtend{
			Key:     d.Key,
			OrderId: order.OrderId,
		}

		return gin.H{
			"status": "EXTEND_ORDER",
			"result": orderExtendDto,
			"msg":    "订单已生成",
		}, nil
	}

	return nil, nil
}

//计算订单
func (d *Order) ComputeOrder() (*ordervo.Compute, error) {
	global.LOG.Info(d.ComputeParam)
	var (
		payPostage     = 0.00
		couponPrice    = 0.00
		deductionPrice = 0.00
		usedIntegral   = 0
		payIntegral    = 0
	)
	cacheDto, err := getCacheOrderInfo(d.Uid, d.Key)
	if err != nil {
		global.LOG.Error("getCacheOrderInfo error", err, "key", d.Key)
		return nil, errors.New("订单已过期，请重新刷新当前页面")
	}
	payPrice := cacheDto.PriceGroup.TotalPrice

	//todo 运费模板
	//目前运费统一0

	//todo 目前不处理门店
	payPrice = payPrice + payPostage

	//todo 秒杀 砍价 拼团

	//todo 优惠券

	//todo 积分抵扣

	return &ordervo.Compute{
		TotalPrice:     cacheDto.PriceGroup.TotalPrice,
		PayPrice:       payPrice,
		PayPostage:     payPostage,
		CouponPrice:    couponPrice,
		DeductionPrice: deductionPrice,
		UseIntegral:    usedIntegral,
		PayIntegral:    payIntegral,
	}, nil

}

//确认订单
func (d *Order) ConfirmOrder() (*ordervo.ConfirmOrder, error) {
	cartService := cart_service.Cart{
		Uid:     d.Uid,
		CartIds: d.CartId,
		Status:  1,
	}
	vo := cartService.GetCartList()
	invalid := vo["invalid"].([]cartVo.Cart)
	valid := vo["valid"].([]cartVo.Cart)
	if len(invalid) > 0 {
		return nil, errors.New("有失效的购物车，请重新提交")
	}
	if len(valid) == 0 {
		return nil, errors.New("请提交购买的商品")
	}

	var (
		deduction      = false //抵扣
		enableIntegral = true  //积分
		userAddress    models.UserAddress
	)
	//获取默认地址
	global.Db.Model(&models.UserAddress{}).
		Where("uid = ?", d.Uid).
		Where("is_default = ?", 1).
		First(&userAddress)
	priceGroup := getOrderPriceGroup(valid)
	cacheKey := cacheOrderInfo(d.Uid, valid, priceGroup, orderDto.Other{})
	//优惠券 todo
	var user userVO.User

	e := copier.Copy(&user, d.User)
	if e != nil {
		global.LOG.Error(e)
	}
	return &ordervo.ConfirmOrder{
		AddressInfo:    userAddress,
		CartInfo:       valid,
		PriceGroup:     priceGroup,
		UserInfo:       user,
		OrderKey:       cacheKey,
		Deduction:      deduction,
		EnableIntegral: enableIntegral,
	}, nil

}

func cacheOrderInfo(uid int64, cartInfo []cartVo.Cart, priceGroup orderDto.PriceGroup, other orderDto.Other) string {
	uuid := ksuid.New()
	key := uuid.String()
	orderCache := orderDto.Cache{
		CartInfo:   cartInfo,
		PriceGroup: priceGroup,
		Other:      other,
	}
	newKey := constant.OrderInfo + strconv.FormatInt(uid, 10) + key
	orderCacheVal, _ := json.Marshal(orderCache)
	err := cache.GetRedisClient(cache.DefaultRedisClient).Set(newKey, orderCacheVal, 15*time.Minute)
	if err != nil {
		global.LOG.Error("cacheOrderInfo error ", err, "key", key)
	}
	return key
}

func getCacheOrderInfo(uid int64, key string) (orderDto.Cache, error) {
	newKey := constant.OrderInfo + strconv.FormatInt(uid, 10) + key
	val, err := cache.GetRedisClient(cache.DefaultRedisClient).GetStr(newKey)
	if err != nil {
		return orderDto.Cache{}, err
	}
	var orderCache orderDto.Cache
	json.Unmarshal([]byte(val), &orderCache)
	return orderCache, nil
}

func delCacheOrderInfo(uid int64, key string) {
	newKey := constant.OrderInfo + strconv.FormatInt(uid, 10) + key
	err := cache.GetRedisClient(cache.DefaultRedisClient).Delete(newKey)
	if err != nil {
		global.LOG.Error("redis error ", err, "key", key, "cmd : Del", "client", cache.DefaultRedisClient)
	}
}

func getOrderPriceGroup(cartInfo []cartVo.Cart) orderDto.PriceGroup {
	var (
		//storePostage float64
		//storeFreePostage float64
		totalPrice float64
		costPrice  float64
		//vipPrice float64
		//payIntegral float64
	)
	//计算价格
	for _, val := range cartInfo {
		dc1 := decimal.NewFromFloat(val.TruePrice).Mul(decimal.NewFromFloat(float64(val.CartNum)))
		sum1, _ := dc1.Float64()
		totalPrice = totalPrice + sum1

		dc2 := decimal.NewFromFloat(val.CostPrice).Mul(decimal.NewFromFloat(float64(val.CartNum)))
		sum2, _ := dc2.Float64()
		costPrice = costPrice + sum2
		//
		//dc3 := decimal.NewFromFloat(val.VipTruePrice).Mul(decimal.NewFromFloat(float64(val.CartNum)))
		//sum3,_ := dc3.Float64()
		//vipPrice = vipPrice + sum3

	}

	return orderDto.PriceGroup{
		//StoreFreePostage: storeFreePostage,
		TotalPrice: totalPrice,
		CostPrice:  costPrice,
	}
}

func (d *Order) GetAll() vo.ResultList {
	maps := make(map[string]interface{})
	if d.Name != "" {
		maps["name"] = d.Name
	}
	if d.Enabled >= 0 {
		maps["is_show"] = d.Enabled
	}
	switch d.IntType {
	case -9:
	case 0:
		maps["paid"] = 0
		maps["refund_status"] = 0
		maps["status"] = 0

	case 1:
		maps["paid"] = 1
		maps["refund_status"] = 0
		maps["status"] = 0
	case 2:
		maps["paid"] = 1
		maps["refund_status"] = 0
		maps["status"] = 1
	case 3:
		maps["paid"] = 1
		maps["refund_status"] = 0
		maps["status"] = 2
	case 4:
		maps["paid"] = 1
		maps["refund_status"] = 0
		maps["status"] = 3
	case -1:
		maps["paid"] = 1
		maps["refund_status"] = 1
	case -2:
		maps["paid"] = 0
		maps["refund_status"] = 2
	}

	total, list := models.GetAdminAllOrder(d.PageNum, d.PageSize, maps)

	var (
		orderInfoList []models.StoreOrderCartInfo
		cart          cartVo.Cart
		newList       []models.StoreOrder
	)
	for _, order := range list {
		global.Db.Model(&models.StoreOrderCartInfo{}).Where("oid = ?", order.Id).Find(&orderInfoList)
		cartInfo := make([]cartVo.Cart, 0)
		for _, orderInfo := range orderInfoList {
			json.Unmarshal([]byte(orderInfo.CartInfo), &cart)
			cartInfo = append(cartInfo, cart)
		}
		order.CartInfo = cartInfo

		_status := orderStatus(order.Paid, order.Status, order.RefundStatus)
		order.OrderStatus = _status
		order.OrderStatusName = orderStatusStr(order.Paid, order.Status, order.ShippingType, order.RefundStatus)
		order.PayTypeName = payTypeName(order.PayType, order.Paid)
		//global.LOG.Info(order.CartInfo)

		newList = append(newList, order)
	}

	return vo.ResultList{Content: newList, TotalElements: total}
}

func (d *Order) GetUseCursor(nextID int64) vo.CursorResultList {
	list := models.GetOrderUseCursor(d.Uid, nextID, d.PageSize)
	var (
		orderInfoList []models.StoreOrderCartInfo
		cart          cartVo.Cart
		newList       []models.StoreOrder
	)
	var newNextID int64
	if len(list) == d.PageSize {
		newNextID = list[d.PageSize-1].Id
	}
	for _, order := range list {
		global.Db.Model(&models.StoreOrderCartInfo{}).Where("oid = ?", order.Id).Find(&orderInfoList)
		cartInfo := make([]cartVo.Cart, 0)
		for _, orderInfo := range orderInfoList {
			json.Unmarshal([]byte(orderInfo.CartInfo), &cart)
			cartInfo = append(cartInfo, cart)
		}
		order.CartInfo = cartInfo

		_status := orderStatus(order.Paid, order.Status, order.RefundStatus)
		order.OrderStatus = _status
		order.OrderStatusName = orderStatusStr(order.Paid, order.Status, order.ShippingType, order.RefundStatus)
		order.PayTypeName = payTypeName(order.PayType, order.Paid)
		//global.LOG.Info(order.CartInfo)

		newList = append(newList, order)
	}

	return vo.CursorResultList{Content: newList, NextID: newNextID}
}

func orderStatus(paid, status, refundStatus int) int {
	//todo  1-未付款 2-未发货 3-退款中 4-待收货 5-待评价 6-已完成 7-已退款
	_status := 0

	if paid == 0 && status == 0 && refundStatus == 0 {
		_status = 1
	} else if paid == 1 && status == 0 && refundStatus == 0 {
		_status = 2
	} else if paid == 1 && refundStatus == 1 {
		_status = 3
	} else if paid == 1 && status == 1 && refundStatus == 0 {
		_status = 4
	} else if paid == 1 && status == 2 && refundStatus == 0 {
		_status = 5
	} else if paid == 1 && status == 3 && refundStatus == 0 {
		_status = 6
	} else if paid == 1 && refundStatus == 2 {
		_status = 7
	}

	return _status

}

func orderStatusStr(paid, status, shippingType, refundStatus int) string {
	statusName := ""
	if paid == 0 && status == 0 {
		statusName = "未支付"
	} else if paid == 1 && status == 0 && shippingType == 1 && refundStatus == 0 {
		statusName = "未发货"
	} else if paid == 1 && status == 0 && shippingType == 2 && refundStatus == 0 {
		statusName = "未核销"
	} else if paid == 1 && status == 1 && shippingType == 1 && refundStatus == 0 {
		statusName = "待收货"
	} else if paid == 1 && status == 1 && shippingType == 2 && refundStatus == 0 {
		statusName = "未核销"
	} else if paid == 1 && status == 2 && refundStatus == 0 {
		statusName = "待评价"
	} else if paid == 1 && status == 3 && refundStatus == 0 {
		statusName = "已完成"
	} else if paid == 1 && refundStatus == 1 {
		statusName = "退款中"
	} else if paid == 1 && refundStatus == 2 {
		statusName = "已退款"
	}

	return statusName
}

func payTypeName(pay_type string, paid int) string {
	payTypeName := "未支付"
	if paid == 1 {
		switch pay_type {
		case "weixin":
			payTypeName = "微信支付"
		case "yue":
			payTypeName = "余额支付"
		case "integral":
			payTypeName = "积分兑换"
		}
	}

	return payTypeName
}

func (d *Order) Del() error {
	return models.DelByStoreOrder(d.Ids)
}

func (d *Order) Save() error {
	return models.UpdateByStoreOrder(d.M)
}

func (d *Order) Deliver() error {
	if d.M.Status != 0 {
		return errors.New("订单状态错误")
	}
	var express models.Express
	err := global.Db.Model(&models.Express{}).Where("name = ?", d.M.DeliveryName).First(&express).Error
	if err != nil {
		return errors.New("请先添加快递公司")
	}
	global.LOG.Info(d.M.DeliveryId)
	d.M.Status = 1
	d.M.DeliverySn = express.Code
	return models.UpdateByStoreOrder(d.M)
}

func (d *Order) OrderEvent(operation string) {
	orderMsg := models.OrderMsg{Operation: operation, StoreOrder: d.M}
	msg, err := json.Marshal(orderMsg)
	if err != nil {
		global.LOG.Error("json.Marshal error", d)
		return
	}
	p, o, err := mq.GetKafkaSyncProducer(mq.DefaultKafkaSyncProducer).Send(&sarama.ProducerMessage{Key: mq.KafkaMsgValueStrEncoder(strconv.FormatInt(d.Uid, 10)),
		Value: mq.KafkaMsgValueEncoder(msg), Topic: orderEnum.Topic})
	if err != nil {
		global.LOG.Error("KafkaSyncProducer error", err, "partion : ", p, "offset : ", o)
	}
}
