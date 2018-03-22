package protobuf

import "fmt"
import "github.com/couchbase/indexing/secondary/logging"
import c "github.com/couchbase/indexing/secondary/common"
import mcd "github.com/couchbase/indexing/secondary/dcp/transport"
import mc "github.com/couchbase/indexing/secondary/dcp/transport/client"
import "github.com/couchbase/eventing/service_manager"

type IndexJSEvaluator struct {
	instance *IndexInst
	version  FeedVersion
	J        *JSEvaluate
}

func NewIndexJSEvaluator(instance *IndexInst,
	version FeedVersion) (*IndexJSEvaluator, error) {
		
	ie := &IndexJSEvaluator{instance: instance, version: version}
	funcname:=instance.GetDefinition().GetFuncName()
	code,err:=servicemanager.GetCode(funcname)
	logging.Infof("CODE IS %v",code)
	if err!=nil{
		logging.Infof("ERROR %v",err)
		return nil,err
	}
	J := NewJSEvaluator(funcname,code)
        J.Compile()
        ie.J = J
	return ie, nil //Compilation error or something
}

func (ie *IndexJSEvaluator) Bucket() string {
	return ie.instance.GetDefinition().GetBucket()
}

func (ie *IndexJSEvaluator) StreamBeginData(
	vbno uint16, vbuuid, seqno uint64) (data interface{}) {

	bucket := ie.Bucket()
	kv := c.NewKeyVersions(seqno, nil, 1, 0 /*ctime*/)
	kv.AddStreamBegin()
	return &c.DataportKeyVersions{bucket, vbno, vbuuid, kv}
}

func (ie *IndexJSEvaluator) SyncData(
	vbno uint16, vbuuid, seqno uint64) (data interface{}) {

	bucket := ie.Bucket()
	kv := c.NewKeyVersions(seqno, nil, 1, 0 /*ctime*/)
	kv.AddSync()
	return &c.DataportKeyVersions{bucket, vbno, vbuuid, kv}
}

func (ie *IndexJSEvaluator) SnapshotData(
	m *mc.DcpEvent, vbno uint16, vbuuid, seqno uint64) (data interface{}) {

	bucket := ie.Bucket()
	kv := c.NewKeyVersions(seqno, nil, 1, m.Ctime)
	kv.AddSnapshot(m.SnapshotType, m.SnapstartSeq, m.SnapendSeq)
	return &c.DataportKeyVersions{bucket, vbno, vbuuid, kv}
}

func (ie *IndexJSEvaluator) StreamEndData(
	vbno uint16, vbuuid, seqno uint64) (data interface{}) {

	bucket := ie.Bucket()
	kv := c.NewKeyVersions(seqno, nil, 1, 0 /*ctime*/)
	kv.AddStreamEnd()
	return &c.DataportKeyVersions{bucket, vbno, vbuuid, kv}
}

func (ie *IndexJSEvaluator) TransformRoute(
	vbuuid uint64, m *mc.DcpEvent, data map[string]interface{},
	encodeBuf []byte) ([]byte, error) {
	var err error
	defer func() { // panic safe
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()

	if ie.version < FeedVersion_watson {
		encodeBuf = nil
	}

	var npkey /*new-partition*/, opkey /*old-partition*/, nkey, okey []byte
	var newBuf []byte
	instn := ie.instance

	defn := instn.Definition
	retainDelete := m.HasXATTR() && defn.GetRetainDeletedXATTR()
	retainDelete = retainDelete && (m.Opcode == mcd.DCP_DELETION || m.Opcode == mcd.DCP_EXPIRATION)
	opcode := m.Opcode
	if retainDelete {
		// TODO: Replace with isMetaIndex()
		m.TreatAsJSON()
		opcode = mcd.DCP_MUTATION
	}

	meta := dcpEvent2Meta(m)
	where:= true
	if len(m.Value) > 0 {
		nkey=ie.J.Run(m.Key, m.Value, meta, encodeBuf)
	}
	if len(m.OldValue) > 0 {
		okey=ie.J.Run(m.Key, m.OldValue, meta, encodeBuf)
	}
	if nkey == nil && okey == nil {
		where = false
	}

	vbno, seqno := m.VBucket, m.Seqno
	uuid := instn.GetInstId()

	bucket := ie.Bucket()
	/*
	logging.LazyTrace(func() string {
		return fmt.Sprintf("inst: %v where: %v (pkey: %v) key: %v\n", uuid, where,
			logging.TagUD(string(npkey)), logging.TagUD(string(nkey)))
	})
	*/	
	switch opcode {
	case mcd.DCP_MUTATION:
		// FIXME: TODO: where clause is not used to for optimizing out messages
		// not passing the where clause. For this we need a gaurantee that
		// where clause will be defined only on immutable fields.	
		if where { // WHERE predicate, sent upsert only if where is true.
			raddrs := instn.UpsertEndpoints(m, npkey, nkey, okey)
			if len(raddrs) != 0 {
				for _, raddr := range raddrs {
					dkv, ok := data[raddr].(*c.DataportKeyVersions)
					if !ok {
						kv := c.NewKeyVersions(seqno, m.Key, 4, m.Ctime)
						kv.AddUpsert(uuid, nkey, okey, npkey)
						dkv = &c.DataportKeyVersions{bucket, vbno, vbuuid, kv}
					} else {
						dkv.Kv.AddUpsert(uuid, nkey, okey, npkey)
					}
					data[raddr] = dkv
				}
			} else {
				// send upsertDeletion if cannot find an endpoint that can accept this mutation
				// for the given feed
				raddrs := instn.UpsertDeletionEndpoints(m, npkey, nkey, okey)
				for _, raddr := range raddrs {
					dkv, ok := data[raddr].(*c.DataportKeyVersions)
					if !ok {
						kv := c.NewKeyVersions(seqno, m.Key, 4, m.Ctime)
						kv.AddUpsertDeletion(uuid, okey, npkey)
						dkv = &c.DataportKeyVersions{bucket, vbno, vbuuid, kv}
					} else {
						dkv.Kv.AddUpsertDeletion(uuid, okey, npkey)
					}
					data[raddr] = dkv
				}
			}
		} else { // if WHERE is false, broadcast upsertdelete.
			// NOTE: downstream can use upsertdelete and immutable flag
			// to optimize out back-index lookup.
			raddrs := instn.UpsertDeletionEndpoints(m, npkey, nkey, okey)
			for _, raddr := range raddrs {
				dkv, ok := data[raddr].(*c.DataportKeyVersions)
				if !ok {
					kv := c.NewKeyVersions(seqno, m.Key, 4, m.Ctime)
					kv.AddUpsertDeletion(uuid, okey, npkey)
					dkv = &c.DataportKeyVersions{bucket, vbno, vbuuid, kv}
				} else {
					dkv.Kv.AddUpsertDeletion(uuid, okey, npkey)
				}
				data[raddr] = dkv
			}
		}

	case mcd.DCP_DELETION, mcd.DCP_EXPIRATION:
		// Delete shall be broadcasted if old-key is not available.
		raddrs := instn.DeletionEndpoints(m, opkey, okey)
		for _, raddr := range raddrs {
			dkv, ok := data[raddr].(*c.DataportKeyVersions)
			if !ok {
				kv := c.NewKeyVersions(seqno, m.Key, 4, m.Ctime)
				kv.AddDeletion(uuid, okey, npkey)
				dkv = &c.DataportKeyVersions{bucket, vbno, vbuuid, kv}
			} else {
				dkv.Kv.AddDeletion(uuid, okey, npkey)
			}
			data[raddr] = dkv
		}
	}
	return newBuf, nil
}

/*
func Equalise(npkey [][]byte,nkey [][]byte, okey [][]byte)([][]byte,[][]byte,[][]byte,int){
	maxlength:=int(math.Max(float64(len(npkey)),math.Max(float64(len(okey)),float64(len(nkey)))))
	npkey=fill(npkey,maxlength)
	nkey=fill(nkey,maxlength)
	okey=fill(okey,maxlength)
	return npkey,nkey,okey,maxlength
}

func fill(arr [][]byte,length int) [][]byte{
	for i:=len(arr);i<length;i++{
		arr=append(arr,[]byte{})
	}
	return arr
}

func getCode(details []byte) string{
	var v interface{}
	json.Unmarshal(details,&v)
	maps:=v.(map[string]interface{})
	logging.Infof("METAKV %v",maps)
	return maps["appcode"].(string)
}
*/
